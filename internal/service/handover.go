package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
	"operan-be/internal/langflow"
	"operan-be/internal/sse"
)

func (s *Service) StartShift(ctx context.Context, profileID, userID string) (*db.Shift, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	sh, err := s.Store.StartShift(ctx, p.ID, userID, s.now())
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.ShiftChanged, p.ID, userID, sh)
	return sh, nil
}

func (s *Service) requireShift(ctx context.Context, shiftID, userID string) (*db.Shift, *db.Profile, error) {
	sh, err := s.Store.ShiftByID(ctx, shiftID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil, apperr.NotFound("Giliran jaga tidak ditemukan.")
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.RequireProfile(ctx, sh.CareProfileID, userID)
	if err != nil {
		return nil, nil, err
	}
	return sh, p, nil
}

// HandoverDraft is shown in the "Selesai jaga" sheet for editing before handing over.
type HandoverDraft struct {
	ShiftID      string    `json:"shift_id"`
	StartedAt    time.Time `json:"started_at"`
	EndsAt       time.Time `json:"ends_at"`
	Summary      string    `json:"summary"`
	PendingItems []string  `json:"pending_items"`
	WatchItems   []string  `json:"watch_items"`
	AIGenerated  bool      `json:"ai_generated"`
}

func (s *Service) HandoverDraft(ctx context.Context, shiftID, userID string) (*HandoverDraft, error) {
	sh, p, err := s.requireShift(ctx, shiftID, userID)
	if err != nil {
		return nil, err
	}
	if sh.EndedAt != nil {
		return nil, apperr.Conflict("SHIFT_ENDED", "Giliran ini sudah selesai.")
	}
	now := s.now()
	in, pending, err := s.handoverInput(ctx, p, sh, now)
	if err != nil {
		return nil, err
	}
	d := &HandoverDraft{ShiftID: sh.ID, StartedAt: sh.StartedAt, EndsAt: now, PendingItems: pending, WatchItems: []string{}}
	var out langflow.HandoverOutput
	if err := s.AI.Run(ctx, langflow.Handover, in, &out); err != nil || ContainsForbidden(out.Summary) {
		s.logErr("handover flow", err)
		d.Summary = ""
		return d, nil
	}
	d.Summary, d.WatchItems, d.AIGenerated = out.Summary, out.WatchItems, true
	if len(out.PendingItems) > 0 {
		d.PendingItems = out.PendingItems
	}
	return d, nil
}

// handoverInput gathers everything that happened during the shift. Pending items are
// computed deterministically so they are correct even if the model is wrong.
func (s *Service) handoverInput(ctx context.Context, p *db.Profile, sh *db.Shift, now time.Time) (langflow.HandoverInput, []string, error) {
	logs, err := s.Store.Logs(ctx, p.ID, sh.StartedAt, now.Add(time.Second), "", 300)
	if err != nil {
		return langflow.HandoverInput{}, nil, err
	}
	_, dayEnd := DayBounds(now, s.Loc)
	if err := s.EnsureDoses(ctx, p.ID, now); err != nil {
		return langflow.HandoverInput{}, nil, err
	}
	doses, err := s.Store.DosesForProfile(ctx, p.ID, sh.StartedAt.Add(-time.Hour), dayEnd)
	if err != nil {
		return langflow.HandoverInput{}, nil, err
	}
	alerts, err := s.Store.Alerts(ctx, p.ID, true, 20)
	if err != nil {
		return langflow.HandoverInput{}, nil, err
	}
	in := langflow.HandoverInput{ProfileType: p.ProfileType, ProfileName: p.DisplayName(),
		Caregiver: deref(sh.CaregiverName, ""), ShiftStart: s.localISO(sh.StartedAt), ShiftEnd: s.localISO(now),
		Logs: s.logItems(logs), Alerts: alertItems(alerts)}
	pending := []string{}
	for _, d := range doses {
		if d.Status == "given" && d.GivenAt != nil && d.GivenAt.Before(sh.StartedAt) {
			continue
		}
		item := s.doseItem(d, now)
		if d.Status == "pending" && d.ScheduledAt != nil {
			// Yang sudah lewat jadwalnya, atau jatuh tempo dalam 3 jam ke depan.
			if d.ScheduledAt.After(now.Add(3 * time.Hour)) {
				continue
			}
			pending = append(pending, strings.TrimSpace(d.MedicationName+" "+d.ScheduledAt.In(s.Loc).Format("15.04")))
		}
		in.Doses = append(in.Doses, item)
	}
	return in, pending, nil
}

type EndShiftInput struct {
	ToUserID        string   `json:"to_user_id"`
	SummaryOverride *string  `json:"summary_override"`
	PendingItems    []string `json:"pending_items"`
	WatchItems      []string `json:"watch_items"`
	AIGenerated     bool     `json:"ai_generated"`
}

func (s *Service) EndShift(ctx context.Context, shiftID, userID string, in EndShiftInput) (*db.Handover, error) {
	sh, p, err := s.requireShift(ctx, shiftID, userID)
	if err != nil {
		return nil, err
	}
	if sh.EndedAt != nil {
		return nil, apperr.Conflict("SHIFT_ENDED", "Giliran ini sudah diserahkan.")
	}
	if in.ToUserID == "" {
		return nil, apperr.Validation("Pilih siapa yang menerima giliran.")
	}
	if role, err := s.Store.MemberRole(ctx, p.FamilyID, in.ToUserID); err != nil {
		return nil, err
	} else if role == "" {
		return nil, apperr.Validation("Penerima harus anggota keluarga ini.")
	}

	h := &db.Handover{CareProfileID: p.ID, FromShiftID: &sh.ID, FromUserID: &userID, ToUserID: &in.ToUserID,
		PendingItems: in.PendingItems, WatchItems: in.WatchItems, AIGenerated: in.AIGenerated}
	if in.SummaryOverride != nil && strings.TrimSpace(*in.SummaryOverride) != "" {
		h.Summary = strings.TrimSpace(*in.SummaryOverride)
	} else {
		draft, err := s.HandoverDraft(ctx, sh.ID, userID)
		if err != nil {
			return nil, err
		}
		h.Summary, h.AIGenerated = draft.Summary, draft.AIGenerated
		if h.PendingItems == nil {
			h.PendingItems = draft.PendingItems
		}
		if h.WatchItems == nil {
			h.WatchItems = draft.WatchItems
		}
		if h.Summary == "" {
			h.Summary = "Tidak ada ringkasan tertulis untuk giliran ini."
		}
	}
	if h.PendingItems == nil {
		h.PendingItems = []string{}
	}
	if h.WatchItems == nil {
		h.WatchItems = []string{}
	}

	now := s.now()
	if err := s.Store.EndShift(ctx, sh.ID, now); err != nil {
		return nil, err
	}
	if err := s.Store.CreateHandover(ctx, h); err != nil {
		return nil, err
	}
	// Penerima otomatis menjadi yang sedang jaga.
	if _, err := s.Store.StartShift(ctx, p.ID, in.ToUserID, now); err != nil {
		return nil, err
	}
	full, err := s.Store.HandoverByID(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.HandoverCreated, p.ID, userID, full)
	s.emit(p.FamilyID, sse.ShiftChanged, p.ID, userID, nil)
	return full, nil
}

func (s *Service) LatestHandover(ctx context.Context, profileID, userID string) (*db.Handover, error) {
	if _, err := s.RequireProfile(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.Store.LatestHandover(ctx, profileID)
}

func (s *Service) MarkHandoverRead(ctx context.Context, handoverID, userID string) (*db.Handover, error) {
	h, err := s.Store.HandoverByID(ctx, handoverID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apperr.NotFound("Operan tidak ditemukan.")
	}
	if err != nil {
		return nil, err
	}
	p, err := s.RequireProfile(ctx, h.CareProfileID, userID)
	if err != nil {
		return nil, err
	}
	if err := s.Store.MarkHandoverRead(ctx, h.ID); err != nil {
		return nil, err
	}
	h, err = s.Store.HandoverByID(ctx, h.ID)
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.HandoverRead, p.ID, userID, h)
	return h, nil
}

// ---------- payload helpers shared by flows ----------

func (s *Service) logItems(logs []db.CareLog) []langflow.LogItem {
	out := make([]langflow.LogItem, 0, len(logs))
	for _, l := range logs {
		out = append(out, langflow.LogItem{ID: l.ID, Type: l.LogType, Value: l.Value, RecordedAt: s.localISO(l.RecordedAt), By: deref(l.AuthorName, "")})
	}
	return out
}

func (s *Service) doseItem(d db.Dose, now time.Time) langflow.DoseItem {
	it := langflow.DoseItem{ID: d.ID, Medication: d.MedicationName, Kind: d.Kind, Status: d.Status, By: deref(d.GivenByName, "")}
	if d.ScheduledAt != nil {
		it.ScheduledAt = s.localISO(*d.ScheduledAt)
	}
	if d.GivenAt != nil {
		it.GivenAt = s.localISO(*d.GivenAt)
	}
	if IsMissed(d.Status, d.ScheduledAt, now) {
		it.Status = "missed"
	}
	return it
}

func (s *Service) doseItems(doses []db.Dose) []langflow.DoseItem {
	now := s.now()
	out := make([]langflow.DoseItem, 0, len(doses))
	for _, d := range doses {
		out = append(out, s.doseItem(d, now))
	}
	return out
}

func alertItems(alerts []db.Alert) []langflow.AlertItem {
	out := make([]langflow.AlertItem, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, langflow.AlertItem{Severity: a.Severity, Title: a.Title, Message: a.Message})
	}
	return out
}
