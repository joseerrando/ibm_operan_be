package service

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
	"operan-be/internal/sse"
)

// ---------- medications ----------

type MedicationInput struct {
	Name               *string   `json:"name"`
	Kind               *string   `json:"kind"`
	DoseLabel          *string   `json:"dose_label"`
	ScheduleTimes      *[]string `json:"schedule_times"`
	IsPRN              *bool     `json:"is_prn"`
	MinIntervalMinutes *int      `json:"min_interval_minutes"`
	ClearInterval      bool      `json:"clear_interval"`
	StartDate          *string   `json:"start_date"`
	EndDate            *string   `json:"end_date"`
	IsActive           *bool     `json:"is_active"`
}

func (in MedicationInput) apply(m *db.Medication) error {
	if in.Name != nil {
		m.Name = strings.TrimSpace(*in.Name)
	}
	if in.Kind != nil {
		m.Kind = *in.Kind
	}
	if in.DoseLabel != nil {
		m.DoseLabel = strings.TrimSpace(*in.DoseLabel)
	}
	if in.IsPRN != nil {
		m.IsPRN = *in.IsPRN
	}
	if in.ScheduleTimes != nil {
		var times []string
		seen := map[string]bool{}
		for _, t := range *in.ScheduleTimes {
			n, err := NormalizeClock(t)
			if err != nil {
				return apperr.Validation(err.Error())
			}
			if !seen[n] {
				seen[n] = true
				times = append(times, n)
			}
		}
		sort.Strings(times)
		m.ScheduleTimes = times
	}
	if in.ClearInterval {
		m.MinIntervalMinutes = nil
	} else if in.MinIntervalMinutes != nil {
		v := *in.MinIntervalMinutes
		if v <= 0 {
			m.MinIntervalMinutes = nil
		} else {
			m.MinIntervalMinutes = &v
		}
	}
	for _, d := range []struct {
		src *string
		dst **string
	}{{in.StartDate, &m.StartDate}, {in.EndDate, &m.EndDate}} {
		if d.src == nil {
			continue
		}
		v := strings.TrimSpace(*d.src)
		if v == "" {
			*d.dst = nil
			continue
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return apperr.Validation("Tanggal harus berformat YYYY-MM-DD.")
		}
		*d.dst = &v
	}
	if in.IsActive != nil {
		m.IsActive = *in.IsActive
	}

	if m.Name == "" {
		return apperr.Validation("Nama obat atau vitamin wajib diisi.")
	}
	if m.Kind != "obat" && m.Kind != "vitamin" && m.Kind != "suplemen" {
		return apperr.Validation("Jenis harus obat, vitamin, atau suplemen.")
	}
	if m.DoseLabel == "" {
		return apperr.Validation("Takaran wajib diisi sesuai anjuran dokter atau kemasan.")
	}
	if m.IsPRN {
		m.ScheduleTimes = []string{}
	} else if len(m.ScheduleTimes) == 0 {
		return apperr.Validation("Isi minimal satu jam pemberian, atau pilih \"Bila perlu\".")
	}
	if m.MinIntervalMinutes != nil && *m.MinIntervalMinutes > 72*60 {
		return apperr.Validation("Jarak minimal antar pemberian maksimal 72 jam.")
	}
	if m.StartDate != nil && m.EndDate != nil && *m.EndDate < *m.StartDate {
		return apperr.Validation("Tanggal selesai tidak boleh sebelum tanggal mulai.")
	}
	return nil
}

func (s *Service) CreateMedication(ctx context.Context, profileID, userID string, in MedicationInput) (*db.Medication, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	m := &db.Medication{CareProfileID: p.ID, IsActive: true}
	if err := in.apply(m); err != nil {
		return nil, err
	}
	if err := s.Store.CreateMedication(ctx, m); err != nil {
		return nil, err
	}
	if err := s.generateFor(ctx, m, s.now()); err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.MedChanged, p.ID, userID, map[string]string{"medication_id": m.ID})
	return m, nil
}

func (s *Service) UpdateMedication(ctx context.Context, medID, userID string, in MedicationInput) (*db.Medication, error) {
	m, p, err := s.RequireMedication(ctx, medID, userID)
	if err != nil {
		return nil, err
	}
	if err := in.apply(m); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateMedication(ctx, m); err != nil {
		return nil, err
	}
	// Jadwal berubah: buang slot masa depan yang belum disentuh, lalu buat ulang.
	now := s.now()
	if err := s.Store.DeletePendingAfter(ctx, m.ID, now); err != nil {
		return nil, err
	}
	if m.IsActive {
		if err := s.generateFor(ctx, m, now); err != nil {
			return nil, err
		}
	}
	s.emit(p.FamilyID, sse.MedChanged, p.ID, userID, map[string]string{"medication_id": m.ID})
	return m, nil
}

func (s *Service) Medications(ctx context.Context, profileID, userID string) ([]db.Medication, error) {
	if _, err := s.RequireProfile(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.Store.MedicationsByProfile(ctx, profileID, true)
}

// ---------- daily dose generation ----------

// generateFor creates today's slots. Slots already in the past at creation time are
// still created so the family can mark them; the missed worker only alerts later ones.
func (s *Service) generateFor(ctx context.Context, m *db.Medication, day time.Time) error {
	if m.IsPRN || !m.IsActive {
		return nil
	}
	for _, slot := range SlotsForDate(m.ScheduleTimes, m.StartDate, m.EndDate, day, s.Loc) {
		if slot.Before(m.CreatedAt.Add(-MissedAfter)) && sameLocalDay(slot, m.CreatedAt, s.Loc) {
			// Obat baru dibuat siang hari: slot pagi yang sudah lewat tidak dianggap terlewat.
			continue
		}
		if err := s.Store.InsertPendingDose(ctx, m.ID, slot); err != nil {
			return err
		}
	}
	return nil
}

func sameLocalDay(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// EnsureDoses generates slots for the given local day for every scheduled medication of a profile.
func (s *Service) EnsureDoses(ctx context.Context, profileID string, day time.Time) error {
	meds, err := s.Store.MedicationsByProfile(ctx, profileID, true)
	if err != nil {
		return err
	}
	for i := range meds {
		if err := s.generateFor(ctx, &meds[i], day); err != nil {
			return err
		}
	}
	return nil
}

// GenerateAll is run by the worker (and at midnight) for every active scheduled medication.
func (s *Service) GenerateAll(ctx context.Context, day time.Time) error {
	meds, err := s.Store.ActiveScheduledMedications(ctx)
	if err != nil {
		return err
	}
	for i := range meds {
		if err := s.generateFor(ctx, &meds[i], day); err != nil {
			return err
		}
	}
	return nil
}

// ---------- dose views ----------

// DoseView decorates a dose row with what the Today screen needs.
type DoseView struct {
	db.Dose
	DoseLabel          string     `json:"dose_label"`
	IsPRN              bool       `json:"is_prn"`
	MinIntervalMinutes *int       `json:"min_interval_minutes"`
	Missed             bool       `json:"missed"`
	AllowedFrom        *time.Time `json:"allowed_from"`
}

func (s *Service) DosesForDate(ctx context.Context, profileID, userID string, day time.Time) ([]DoseView, error) {
	if _, err := s.RequireProfile(ctx, profileID, userID); err != nil {
		return nil, err
	}
	return s.doseViews(ctx, profileID, day)
}

// DosesRange returns every dose in [from, to) in one query (used by the history screen).
func (s *Service) DosesRange(ctx context.Context, profileID, userID string, from, to time.Time) ([]DoseView, error) {
	if _, err := s.RequireProfile(ctx, profileID, userID); err != nil {
		return nil, err
	}
	if to.Sub(from) > 62*24*time.Hour {
		return nil, apperr.Validation("Rentang maksimal 2 bulan.")
	}
	if err := s.EnsureDoses(ctx, profileID, s.now()); err != nil {
		return nil, err
	}
	return s.doseViewsRange(ctx, profileID, from, to)
}

func (s *Service) doseViews(ctx context.Context, profileID string, day time.Time) ([]DoseView, error) {
	if err := s.EnsureDoses(ctx, profileID, day); err != nil {
		return nil, err
	}
	from, to := DayBounds(day, s.Loc)
	return s.doseViewsRange(ctx, profileID, from, to)
}

func (s *Service) doseViewsRange(ctx context.Context, profileID string, from, to time.Time) ([]DoseView, error) {
	doses, err := s.Store.DosesForProfile(ctx, profileID, from, to)
	if err != nil {
		return nil, err
	}
	meds, err := s.Store.MedicationsByProfile(ctx, profileID, false)
	if err != nil {
		return nil, err
	}
	medByID := map[string]db.Medication{}
	for _, m := range meds {
		medByID[m.ID] = m
	}
	now := s.now()
	out := make([]DoseView, 0, len(doses))
	for _, d := range doses {
		m := medByID[d.MedicationID]
		v := DoseView{Dose: d, DoseLabel: m.DoseLabel, IsPRN: m.IsPRN, MinIntervalMinutes: m.MinIntervalMinutes,
			Missed: IsMissed(d.Status, d.ScheduledAt, now)}
		if d.Status == "given" && d.GivenAt != nil && m.MinIntervalMinutes != nil {
			af := d.GivenAt.Add(time.Duration(*m.MinIntervalMinutes) * time.Minute)
			v.AllowedFrom = &af
		}
		out = append(out, v)
	}
	return out, nil
}

// PRNStatus answers "boleh lagi mulai jam berapa?" for an as-needed medication.
type PRNStatus struct {
	Medication      db.Medication `json:"medication"`
	LastDoseID      *string       `json:"last_dose_id"`
	LastGivenAt     *time.Time    `json:"last_given_at"`
	LastGivenByName *string       `json:"last_given_by_name"`
	AllowedFrom     *time.Time    `json:"allowed_from"`
	CanGiveNow      bool          `json:"can_give_now"`
}

func (s *Service) prnStatuses(ctx context.Context, meds []db.Medication) ([]PRNStatus, error) {
	now := s.now()
	out := []PRNStatus{}
	for _, m := range meds {
		if !m.IsPRN || !m.IsActive {
			continue
		}
		st := PRNStatus{Medication: m, CanGiveNow: true}
		last, err := s.Store.LastGivenDose(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		if last != nil {
			st.LastDoseID, st.LastGivenAt, st.LastGivenByName = &last.ID, last.GivenAt, last.GivenByName
			if m.MinIntervalMinutes != nil && last.GivenAt != nil {
				af := last.GivenAt.Add(time.Duration(*m.MinIntervalMinutes) * time.Minute)
				st.AllowedFrom = &af
				st.CanGiveNow = !now.Before(af)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// ---------- give / skip / undo ----------

type GiveInput struct {
	Force   bool    `json:"force"`
	Reason  *string `json:"reason"`
	GivenAt *string `json:"given_at"` // opsional, RFC3339; default sekarang
}

// doubleDoseError builds the 409 DOUBLE_DOSE response with the facts the app shows.
func (s *Service) doubleDoseError(m *db.Medication, c *Conflict) *apperr.Error {
	msg := fmt.Sprintf("%s sudah diberikan oleh %s pukul %s. Yakin ingin mencatat lagi?",
		m.Name, c.LastGivenByName, c.LastGivenAt.In(s.Loc).Format("15.04"))
	return apperr.New(http.StatusConflict, "DOUBLE_DOSE", msg).With(map[string]any{
		"medication_id":        m.ID,
		"medication_name":      m.Name,
		"reason":               c.Reason,
		"last_given_at":        c.LastGivenAt,
		"last_given_by_name":   c.LastGivenByName,
		"min_interval_minutes": c.MinIntervalMinutes,
		"allowed_from":         c.AllowedFrom,
	})
}

// checkConflict gathers the facts for CheckDoubleDose from the database.
func (s *Service) checkConflict(ctx context.Context, m *db.Medication, slot *db.Dose, at time.Time) (*Conflict, error) {
	var slotGiven *GivenRecord
	if slot != nil && slot.Status == "given" && slot.GivenAt != nil {
		slotGiven = &GivenRecord{At: *slot.GivenAt, ByName: deref(slot.GivenByName, "anggota keluarga")}
	}
	var nearby []GivenRecord
	if m.MinIntervalMinutes != nil && *m.MinIntervalMinutes > 0 {
		doses, err := s.Store.GivenDosesNear(ctx, m.ID, at, time.Duration(*m.MinIntervalMinutes)*time.Minute)
		if err != nil {
			return nil, err
		}
		for _, d := range doses {
			if slot != nil && d.ID == slot.ID {
				continue
			}
			nearby = append(nearby, GivenRecord{At: *d.GivenAt, ByName: deref(d.GivenByName, "anggota keluarga")})
		}
	}
	return CheckDoubleDose(m.MinIntervalMinutes, slotGiven, nearby, at), nil
}

func deref(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

func (s *Service) parseGivenAt(raw *string) (time.Time, error) {
	now := s.now()
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return now, nil
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return time.Time{}, apperr.Validation("Waktu pemberian tidak valid.")
	}
	if t.After(now.Add(5 * time.Minute)) {
		return time.Time{}, apperr.Validation("Waktu pemberian tidak boleh di masa depan.")
	}
	return t.UTC(), nil
}

func forcedNote(reason *string) *string {
	note := "Tetap dicatat meski masih dalam jeda"
	if reason != nil && strings.TrimSpace(*reason) != "" {
		note += ": " + strings.TrimSpace(*reason)
	}
	return &note
}

func (s *Service) GiveDose(ctx context.Context, doseID, userID string, in GiveInput) (*db.Dose, error) {
	d, m, p, err := s.RequireDose(ctx, doseID, userID)
	if err != nil {
		return nil, err
	}
	at, err := s.parseGivenAt(in.GivenAt)
	if err != nil {
		return nil, err
	}
	c, err := s.checkConflict(ctx, m, d, at)
	if err != nil {
		return nil, err
	}
	if c != nil && !in.Force {
		return nil, s.doubleDoseError(m, c)
	}
	var note *string
	if c != nil {
		note = forcedNote(in.Reason)
	}
	if d.Status == "given" {
		// Slot sudah terisi dan pengguna memaksa: catat sebagai pemberian tambahan.
		if _, err := s.Store.InsertGivenDose(ctx, m.ID, at, userID, note, true); err != nil {
			return nil, err
		}
	} else if err := s.Store.MarkDoseGiven(ctx, d.ID, at, userID, note, c != nil); err != nil {
		return nil, err
	}
	out, err := s.Store.DoseByID(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.DoseGiven, p.ID, userID, out)
	return out, nil
}

func (s *Service) GivePRN(ctx context.Context, medID, userID string, in GiveInput) (*db.Dose, error) {
	m, p, err := s.RequireMedication(ctx, medID, userID)
	if err != nil {
		return nil, err
	}
	if !m.IsActive {
		return nil, apperr.Validation("Obat ini sudah tidak aktif.")
	}
	at, err := s.parseGivenAt(in.GivenAt)
	if err != nil {
		return nil, err
	}
	c, err := s.checkConflict(ctx, m, nil, at)
	if err != nil {
		return nil, err
	}
	if c != nil && !in.Force {
		return nil, s.doubleDoseError(m, c)
	}
	var note *string
	if c != nil {
		note = forcedNote(in.Reason)
	}
	id, err := s.Store.InsertGivenDose(ctx, m.ID, at, userID, note, c != nil)
	if err != nil {
		return nil, err
	}
	out, err := s.Store.DoseByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.DoseGiven, p.ID, userID, out)
	return out, nil
}

func (s *Service) SkipDose(ctx context.Context, doseID, userID string, note *string) (*db.Dose, error) {
	d, _, p, err := s.RequireDose(ctx, doseID, userID)
	if err != nil {
		return nil, err
	}
	if d.Status == "given" {
		return nil, apperr.Conflict("ALREADY_GIVEN", "Obat ini sudah dicatat diberikan. Batalkan dulu jika keliru.")
	}
	if err := s.Store.SkipDose(ctx, d.ID, userID, note); err != nil {
		return nil, err
	}
	out, err := s.Store.DoseByID(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.DoseSkipped, p.ID, userID, out)
	return out, nil
}

// UndoWindow is how long the "Batalkan" action stays available.
const UndoWindow = 10 * time.Minute

func (s *Service) UndoDose(ctx context.Context, doseID, userID string) (*db.Dose, error) {
	d, _, p, err := s.RequireDose(ctx, doseID, userID)
	if err != nil {
		return nil, err
	}
	if d.Status == "pending" {
		return nil, apperr.Conflict("NOTHING_TO_UNDO", "Tidak ada yang perlu dibatalkan.")
	}
	if d.GivenBy == nil || *d.GivenBy != userID {
		return nil, apperr.New(http.StatusForbidden, "UNDO_NOT_ALLOWED", "Hanya yang mencatat yang bisa membatalkan.")
	}
	if d.MarkedAt == nil || s.now().Sub(*d.MarkedAt) > UndoWindow {
		return nil, apperr.Conflict("UNDO_EXPIRED", "Batas waktu membatalkan (10 menit) sudah lewat.")
	}
	if err := s.Store.UndoDose(ctx, d); err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.DoseUndone, p.ID, userID, map[string]string{"dose_id": d.ID})
	if d.ScheduledAt == nil {
		return nil, nil
	}
	return s.Store.DoseByID(ctx, d.ID)
}

// ---------- missed doses ----------

// CheckMissed creates a "perhatian" alert for every scheduled dose pending > 30 minutes.
func (s *Service) CheckMissed(ctx context.Context, now time.Time) (int, error) {
	doses, err := s.Store.PendingDosesBefore(ctx, now.Add(-MissedAfter))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range doses {
		if !IsMissed(d.Status, d.ScheduledAt, now) {
			continue
		}
		p, err := s.Store.ProfileByID(ctx, d.CareProfileID)
		if err != nil {
			s.logErr("missed: profile", err)
			continue
		}
		clock := d.ScheduledAt.In(s.Loc).Format("15.04")
		day := ""
		if !sameLocalDay(*d.ScheduledAt, now, s.Loc) {
			day = " kemarin"
			if now.Sub(*d.ScheduledAt) > 48*time.Hour {
				day = " " + DateID(d.ScheduledAt.In(s.Loc))
			}
		}
		metric := "obat"
		a := &db.Alert{CareProfileID: p.ID, Source: "rule", Severity: "perhatian", Metric: &metric,
			Title:   fmt.Sprintf("%s jam %s%s belum diberikan", d.MedicationName, clock, day),
			Message: fmt.Sprintf("Jadwal %s untuk %s pukul %s%s belum dicatat. Jika sudah diberikan, catat di layar Hari ini.", d.MedicationName, p.DisplayName(), clock, day),
		}
		if err := s.Store.CreateAlert(ctx, a); err != nil {
			s.logErr("missed: create alert", err)
			continue
		}
		if err := s.Store.MarkMissedAlerted(ctx, d.ID); err != nil {
			s.logErr("missed: mark", err)
		}
		s.emit(p.FamilyID, sse.AlertCreated, p.ID, "", a)
		n++
	}
	return n, nil
}
