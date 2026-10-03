package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
	"operan-be/internal/sse"
)

// ---------- validation ----------

type logValue struct {
	Celsius   *float64 `json:"celsius,omitempty"`
	Systolic  *int     `json:"systolic,omitempty"`
	Diastolic *int     `json:"diastolic,omitempty"`
	Pulse     *int     `json:"pulse,omitempty"`
	MgDl      *float64 `json:"mg_dl,omitempty"`
	Context   *string  `json:"context,omitempty"`
	Portion   *string  `json:"portion,omitempty"`
	Quality   *string  `json:"quality,omitempty"`
	Hours     *float64 `json:"hours,omitempty"`
	Note      *string  `json:"note,omitempty"`
	Text      *string  `json:"text,omitempty"`
}

// ValidateLogValue checks ranges per log_type and returns a canonical JSON value.
func ValidateLogValue(logType string, raw json.RawMessage) (json.RawMessage, error) {
	var v logValue
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil, apperr.Validation("Isi catatan tidak terbaca.")
	}
	var out map[string]any
	switch logType {
	case "suhu":
		if v.Celsius == nil || *v.Celsius < 34 || *v.Celsius > 43 {
			return nil, apperr.Validation("Suhu harus di antara 34 dan 43 °C.")
		}
		out = map[string]any{"celsius": round1(*v.Celsius)}
	case "tensi":
		if v.Systolic == nil || *v.Systolic < 60 || *v.Systolic > 260 {
			return nil, apperr.Validation("Tensi atas (sistolik) harus di antara 60 dan 260.")
		}
		if v.Diastolic == nil || *v.Diastolic < 30 || *v.Diastolic > 160 {
			return nil, apperr.Validation("Tensi bawah (diastolik) harus di antara 30 dan 160.")
		}
		if *v.Diastolic >= *v.Systolic {
			return nil, apperr.Validation("Tensi bawah harus lebih kecil dari tensi atas.")
		}
		out = map[string]any{"systolic": *v.Systolic, "diastolic": *v.Diastolic}
		if v.Pulse != nil {
			if *v.Pulse < 30 || *v.Pulse > 220 {
				return nil, apperr.Validation("Nadi harus di antara 30 dan 220.")
			}
			out["pulse"] = *v.Pulse
		}
	case "gula_darah":
		if v.MgDl == nil || *v.MgDl < 20 || *v.MgDl > 600 {
			return nil, apperr.Validation("Gula darah harus di antara 20 dan 600 mg/dL.")
		}
		ctx := "sewaktu"
		if v.Context != nil {
			ctx = *v.Context
		}
		if ctx != "puasa" && ctx != "sesudah_makan" && ctx != "sewaktu" {
			return nil, apperr.Validation("Waktu pengukuran gula darah tidak dikenal.")
		}
		out = map[string]any{"mg_dl": round1(*v.MgDl), "context": ctx}
	case "makan":
		if v.Portion == nil || !oneOf(*v.Portion, "habis", "setengah", "sedikit", "tidak_mau") {
			return nil, apperr.Validation("Pilih porsi makan: habis, setengah, sedikit, atau tidak mau.")
		}
		out = map[string]any{"portion": *v.Portion, "note": trimPtr(v.Note, 300)}
	case "tidur":
		if v.Quality == nil || !oneOf(*v.Quality, "nyenyak", "gelisah", "sulit") {
			return nil, apperr.Validation("Pilih kualitas tidur: nyenyak, gelisah, atau sulit.")
		}
		out = map[string]any{"quality": *v.Quality}
		if v.Hours != nil {
			if *v.Hours < 0 || *v.Hours > 24 {
				return nil, apperr.Validation("Lama tidur harus di antara 0 dan 24 jam.")
			}
			out["hours"] = round1(*v.Hours)
		}
	case "bab_bak":
		out = map[string]any{"note": trimPtr(v.Note, 300)}
	case "keluhan", "catatan":
		t := trimPtr(v.Text, 1000)
		if t == "" {
			return nil, apperr.Validation("Tulis isi catatannya dulu.")
		}
		out = map[string]any{"text": t}
	default:
		return nil, apperr.Validation("Jenis catatan tidak dikenal.")
	}
	b, _ := json.Marshal(out)
	return b, nil
}

func round1(f float64) float64 { return float64(int64(f*10+0.5*sign(f))) / 10 }
func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}

func trimPtr(p *string, max int) string {
	if p == nil {
		return ""
	}
	s := strings.TrimSpace(*p)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// ---------- create / list ----------

type LogInput struct {
	LogType    string          `json:"log_type"`
	Value      json.RawMessage `json:"value"`
	RecordedAt *string         `json:"recorded_at"`
}

func (s *Service) CreateLog(ctx context.Context, profileID, userID string, in LogInput, source string) (*db.CareLog, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	return s.createLog(ctx, p, userID, in, source)
}

func (s *Service) createLog(ctx context.Context, p *db.Profile, userID string, in LogInput, source string) (*db.CareLog, error) {
	val, err := ValidateLogValue(in.LogType, in.Value)
	if err != nil {
		return nil, err
	}
	at, err := s.parseGivenAt(in.RecordedAt)
	if err != nil {
		return nil, apperr.Validation("Jam catatan tidak valid atau di masa depan.")
	}
	uid := userID
	l := &db.CareLog{CareProfileID: p.ID, AuthorID: &uid, LogType: in.LogType, Value: val, Source: source, RecordedAt: at}
	if err := s.Store.CreateLog(ctx, l); err != nil {
		return nil, err
	}
	full, err := s.Store.LogByID(ctx, l.ID)
	if err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.LogCreated, p.ID, userID, full)
	s.runRules(ctx, p, full)
	s.maybeTrend(p)
	return full, nil
}

func (s *Service) Logs(ctx context.Context, profileID, userID string, from, to time.Time, logType string) ([]db.CareLog, error) {
	if _, err := s.RequireProfile(ctx, profileID, userID); err != nil {
		return nil, err
	}
	if logType != "" && logType != "catatan" && !metricTypes[logType] {
		return nil, apperr.Validation("Jenis catatan tidak dikenal.")
	}
	return s.Store.Logs(ctx, profileID, from, to, logType, 1000)
}

// ---------- rule alerts ----------

func (s *Service) runRules(ctx context.Context, p *db.Profile, l *db.CareLog) {
	var drafts []AlertDraft
	switch l.LogType {
	case "suhu":
		var v struct{ Celsius float64 }
		_ = json.Unmarshal(l.Value, &v)
		from := l.RecordedAt.AddDate(0, 0, -6)
		recent, err := s.Store.Logs(ctx, p.ID, from, l.RecordedAt.Add(time.Second), "suhu", 500)
		if err != nil {
			s.logErr("rules: temps", err)
			return
		}
		var readings []TempReading
		for _, r := range recent {
			if r.ID == l.ID {
				continue
			}
			var rv struct{ Celsius float64 }
			_ = json.Unmarshal(r.Value, &rv)
			readings = append(readings, TempReading{At: r.RecordedAt, Celsius: rv.Celsius})
		}
		drafts = EvaluateTemperature(p.ProfileType, p.DisplayName(), TempReading{At: l.RecordedAt, Celsius: v.Celsius}, readings, s.Loc)
	case "tensi":
		recent, err := s.Store.RecentLogsOfType(ctx, p.ID, "tensi", 3)
		if err != nil {
			s.logErr("rules: tensi", err)
			return
		}
		var sys []int
		for _, r := range recent {
			var rv struct{ Systolic int }
			_ = json.Unmarshal(r.Value, &rv)
			sys = append(sys, rv.Systolic)
		}
		drafts = EvaluateSystolic(p.ProfileType, p.DisplayName(), sys)
	}
	for _, d := range drafts {
		s.saveAlert(ctx, p, "rule", d)
	}
}

// saveAlert stores an alert unless the same title was raised in the last 24 hours.
func (s *Service) saveAlert(ctx context.Context, p *db.Profile, source string, d AlertDraft) *db.Alert {
	dup, err := s.Store.HasRecentAlert(ctx, p.ID, d.Title, s.now().Add(-24*time.Hour))
	if err != nil || dup {
		return nil
	}
	var metric *string
	if d.Metric != "" {
		metric = &d.Metric
	}
	a := &db.Alert{CareProfileID: p.ID, Source: source, Severity: d.Severity, Title: d.Title, Message: d.Message, Metric: metric}
	if err := s.Store.CreateAlert(ctx, a); err != nil {
		s.logErr("save alert", err)
		return nil
	}
	s.emit(p.FamilyID, sse.AlertCreated, p.ID, "", a)
	return a
}

func (s *Service) Alerts(ctx context.Context, profileID, userID string, all bool) ([]db.Alert, error) {
	if _, err := s.RequireProfile(ctx, profileID, userID); err != nil {
		return nil, err
	}
	alerts, err := s.Store.Alerts(ctx, profileID, !all, 100)
	if err != nil {
		return nil, err
	}
	sortAlerts(alerts)
	return alerts, nil
}

var severityRank = map[string]int{"penting": 3, "perhatian": 2, "info": 1}

func sortAlerts(a []db.Alert) {
	sort.SliceStable(a, func(i, j int) bool {
		if a[i].IsRead != a[j].IsRead {
			return !a[i].IsRead
		}
		if severityRank[a[i].Severity] != severityRank[a[j].Severity] {
			return severityRank[a[i].Severity] > severityRank[a[j].Severity]
		}
		return a[i].CreatedAt.After(a[j].CreatedAt)
	})
}

func (s *Service) MarkAlertRead(ctx context.Context, alertID, userID string) (*db.Alert, error) {
	a, err := s.Store.AlertByID(ctx, alertID)
	if err != nil {
		return nil, apperr.NotFound("Peringatan tidak ditemukan.")
	}
	p, err := s.RequireProfile(ctx, a.CareProfileID, userID)
	if err != nil {
		return nil, err
	}
	if err := s.Store.MarkAlertRead(ctx, a.ID); err != nil {
		return nil, err
	}
	a.IsRead = true
	s.emit(p.FamilyID, sse.AlertRead, p.ID, userID, a)
	return a, nil
}

// ---------- Today aggregate ----------

type LatestMetric struct {
	ID         string          `json:"id"`
	Value      json.RawMessage `json:"value"`
	RecordedAt time.Time       `json:"recorded_at"`
	AuthorName *string         `json:"author_name"`
}

type OnDuty struct {
	ShiftID   string    `json:"shift_id"`
	UserID    *string   `json:"user_id"`
	Name      *string   `json:"name"`
	StartedAt time.Time `json:"started_at"`
}

type TodayResult struct {
	Profile         *db.Profile             `json:"profile"`
	PrimaryMetric   string                  `json:"primary_metric"`
	OnDuty          *OnDuty                 `json:"on_duty"`
	Latest          map[string]LatestMetric `json:"latest"`
	Doses           []DoseView              `json:"doses"`
	PRN             []PRNStatus             `json:"prn"`
	MedicationCount int                     `json:"medication_count"`
	Alerts          []db.Alert              `json:"alerts"`
	Handover        *db.Handover            `json:"handover"`      // operan masuk yang belum dibaca untuk saya
	SentHandover    *db.Handover            `json:"sent_handover"` // operan terakhir yang saya serahkan
	Timeline        []TimelineEvent         `json:"timeline"`
	Now             time.Time               `json:"now"`
}

// PrimaryMetric picks the big number on the Today card.
func PrimaryMetric(p *db.Profile) string {
	pref := []string{"suhu", "tensi", "gula_darah"}
	if p.ProfileType == "lansia" || p.ProfileType == "kronis" {
		pref = []string{"tensi", "gula_darah", "suhu"}
	}
	for _, m := range pref {
		for _, t := range p.TrackedMetrics {
			if t == m {
				return m
			}
		}
	}
	if len(p.TrackedMetrics) > 0 {
		return p.TrackedMetrics[0]
	}
	return "suhu"
}

func (s *Service) Today(ctx context.Context, profileID, userID string) (*TodayResult, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	res := &TodayResult{Profile: p, PrimaryMetric: PrimaryMetric(p), Latest: map[string]LatestMetric{}, Now: now}

	if sh, err := s.Store.ActiveShift(ctx, p.ID); err != nil {
		return nil, err
	} else if sh != nil {
		res.OnDuty = &OnDuty{ShiftID: sh.ID, UserID: sh.CaregiverID, Name: sh.CaregiverName, StartedAt: sh.StartedAt}
	}
	latest, err := s.Store.LatestLogPerType(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range latest {
		if _, seen := res.Latest[l.LogType]; !seen {
			res.Latest[l.LogType] = LatestMetric{ID: l.ID, Value: l.Value, RecordedAt: l.RecordedAt, AuthorName: l.AuthorName}
		}
	}
	if res.Doses, err = s.doseViews(ctx, p.ID, now); err != nil {
		return nil, err
	}
	meds, err := s.Store.MedicationsByProfile(ctx, p.ID, true)
	if err != nil {
		return nil, err
	}
	res.MedicationCount = len(meds)
	if res.PRN, err = s.prnStatuses(ctx, meds); err != nil {
		return nil, err
	}
	if res.Alerts, err = s.Store.Alerts(ctx, p.ID, true, 20); err != nil {
		return nil, err
	}
	sortAlerts(res.Alerts)
	if h, err := s.Store.LatestHandover(ctx, p.ID); err != nil {
		return nil, err
	} else if h != nil {
		if h.ToUserID != nil && *h.ToUserID == userID && h.ReadAt == nil {
			res.Handover = h
		}
		if h.FromUserID != nil && *h.FromUserID == userID {
			res.SentHandover = h
		}
	}
	if res.Timeline, err = s.timeline(ctx, p, now, res.Doses); err != nil {
		return nil, err
	}
	return res, nil
}

// ---------- Timeline for the Day Ribbon ----------

type TimelineEvent struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`   // dose | log
	Label  string    `json:"label"`  // "Penurun panas, diberikan Ibu"
	Status string    `json:"status"` // done | scheduled | attention | skipped
	RefID  string    `json:"ref_id"`
	Short  string    `json:"short"` // label singkat di bawah penanda
}

func (s *Service) Timeline(ctx context.Context, profileID, userID string, day time.Time) ([]TimelineEvent, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	doses, err := s.doseViews(ctx, p.ID, day)
	if err != nil {
		return nil, err
	}
	return s.timeline(ctx, p, day, doses)
}

func (s *Service) timeline(ctx context.Context, p *db.Profile, day time.Time, doses []DoseView) ([]TimelineEvent, error) {
	from, to := DayBounds(day, s.Loc)
	logs, err := s.Store.Logs(ctx, p.ID, from, to, "", 200)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []TimelineEvent{}
	for _, d := range doses {
		ev := TimelineEvent{Kind: "dose", RefID: d.ID, Short: shortName(d.MedicationName)}
		switch {
		case d.Status == "given" && d.GivenAt != nil:
			ev.Time, ev.Status = *d.GivenAt, "done"
			ev.Label = fmt.Sprintf("%s, diberikan %s pukul %s", d.MedicationName, deref(d.GivenByName, "anggota keluarga"), d.GivenAt.In(s.Loc).Format("15.04"))
		case d.Status == "skipped" && d.ScheduledAt != nil:
			ev.Time, ev.Status = *d.ScheduledAt, "skipped"
			ev.Label = fmt.Sprintf("%s pukul %s, dilewati", d.MedicationName, d.ScheduledAt.In(s.Loc).Format("15.04"))
		case d.ScheduledAt != nil:
			ev.Time = *d.ScheduledAt
			ev.Status = "scheduled"
			ev.Label = fmt.Sprintf("%s, jadwal %s", d.MedicationName, d.ScheduledAt.In(s.Loc).Format("15.04"))
			if IsMissed(d.Status, d.ScheduledAt, now) {
				ev.Status = "attention"
				ev.Label = fmt.Sprintf("%s pukul %s belum diberikan", d.MedicationName, d.ScheduledAt.In(s.Loc).Format("15.04"))
			}
		default:
			continue
		}
		out = append(out, ev)
	}
	for _, l := range logs {
		ev := TimelineEvent{Time: l.RecordedAt, Kind: "log", RefID: l.ID, Status: "done"}
		ev.Label, ev.Short, ev.Status = describeLog(l, s.Loc)
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func shortName(n string) string {
	f := strings.Fields(n)
	if len(f) == 0 {
		return n
	}
	return f[0]
}

var logTypeNames = map[string]string{"suhu": "Suhu", "tensi": "Tensi", "gula_darah": "Gula darah", "makan": "Makan",
	"tidur": "Tidur", "bab_bak": "BAB/BAK", "keluhan": "Keluhan", "catatan": "Catatan"}

// describeLog renders a log as (label, short label, ribbon status).
func describeLog(l db.CareLog, loc *time.Location) (string, string, string) {
	var v struct {
		Celsius             float64
		Systolic, Diastolic int
		MgDl                float64 `json:"mg_dl"`
		Portion, Quality    string
		Text, Note          string
	}
	_ = json.Unmarshal(l.Value, &v)
	by := deref(l.AuthorName, "anggota keluarga")
	clock := l.RecordedAt.In(loc).Format("15.04")
	status := "done"
	var what string
	switch l.LogType {
	case "suhu":
		what = fmt.Sprintf("Suhu %s°", FormatDecimal(v.Celsius))
		if v.Celsius >= FeverThreshold {
			status = "attention"
		}
	case "tensi":
		what = fmt.Sprintf("Tensi %d/%d", v.Systolic, v.Diastolic)
		if v.Systolic >= SystolicThreshold {
			status = "attention"
		}
	case "gula_darah":
		what = fmt.Sprintf("Gula darah %s", strings.TrimSuffix(FormatDecimal(v.MgDl), ",0"))
	case "makan":
		what = "Makan " + map[string]string{"habis": "habis", "setengah": "setengah", "sedikit": "sedikit", "tidak_mau": "tidak mau"}[v.Portion]
	case "tidur":
		what = "Tidur " + v.Quality
	case "keluhan", "catatan":
		what = logTypeNames[l.LogType] + ": " + v.Text
	default:
		what = logTypeNames[l.LogType]
	}
	return fmt.Sprintf("%s, dicatat %s pukul %s", what, by, clock), logTypeNames[l.LogType], status
}
