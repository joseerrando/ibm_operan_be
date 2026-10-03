package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
	"operan-be/internal/langflow"
	"operan-be/internal/stt"
)

// ---------- Flow 1: voice ----------

type VoicePreviewLog struct {
	LogType    string          `json:"log_type"`
	Value      json.RawMessage `json:"value"`
	RecordedAt time.Time       `json:"recorded_at"`
}

type VoicePreviewDose struct {
	MedicationID   string    `json:"medication_id"`
	MedicationName string    `json:"medication_name"`
	Kind           string    `json:"kind"`
	GivenAt        time.Time `json:"given_at"`
	Conflict       *Conflict `json:"conflict"`
}

type VoicePreview struct {
	Transcript    string             `json:"transcript"`
	Logs          []VoicePreviewLog  `json:"logs"`
	Doses         []VoicePreviewDose `json:"doses"`
	UnmatchedText string             `json:"unmatched_text"`
	AIAvailable   bool               `json:"ai_available"`
}

const (
	MaxAudioBytes = 2 << 20
	minAudioBytes = 2 << 10
)

// VoicePreview transcribes (unless a transcript is given), extracts, and checks conflicts.
// Nothing is saved: the family confirms on the "Periksa dulu" screen.
func (s *Service) VoicePreview(ctx context.Context, profileID, userID string, audio []byte, mime, transcript string) (*VoicePreview, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	meds, err := s.Store.MedicationsByProfile(ctx, p.ID, true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(transcript) == "" {
		if len(audio) < minAudioBytes {
			return nil, apperr.Validation("Rekaman terlalu pendek. Coba rekam lagi sambil menyebutkan kondisinya.")
		}
		if len(audio) > MaxAudioBytes {
			return nil, apperr.Validation("Rekaman terlalu panjang. Maksimal 60 detik.")
		}
		names := make([]string, 0, len(meds))
		for _, m := range meds {
			names = append(names, m.Name)
		}
		sttCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		transcript, err = s.STT.Transcribe(sttCtx, audio, mime, stt.Hint{ProfileName: p.DisplayName(), ProfileType: p.ProfileType, MedNames: names})
		cancel()
		if err != nil {
			slog.Warn("stt gagal", "err", err)
			return nil, apperr.New(422, "STT_FAILED", "Rekaman tidak terdengar jelas. Coba rekam lagi di tempat yang lebih tenang, atau ketik catatannya.")
		}
	}
	transcript = strings.TrimSpace(transcript)
	res := &VoicePreview{Transcript: transcript, Logs: []VoicePreviewLog{}, Doses: []VoicePreviewDose{}}

	now := s.now()
	in := langflow.VoiceInput{ProfileType: p.ProfileType, ProfileName: p.DisplayName(), Now: s.localISO(now), Transcript: transcript}
	medByID := map[string]*db.Medication{}
	for i := range meds {
		medByID[meds[i].ID] = &meds[i]
		in.Medications = append(in.Medications, langflow.VoiceMed{ID: meds[i].ID, Name: meds[i].Name, Kind: meds[i].Kind})
	}
	var out langflow.VoiceOutput
	aiCtx, cancelAI := context.WithTimeout(ctx, interactiveAIBudget)
	err = s.AI.Run(aiCtx, langflow.ExtractVoice, in, &out)
	cancelAI()
	if err != nil {
		slog.Warn("flow extract_voice gagal, memakai ekstraksi lokal", "err", err)
		out = langflow.VoiceOutput{}
		if s.Fallback == nil || s.Fallback.Run(ctx, langflow.ExtractVoice, in, &out) != nil {
			res.UnmatchedText = transcript
			return res, nil
		}
	}
	res.AIAvailable = true
	res.UnmatchedText = strings.TrimSpace(out.UnmatchedText)

	for _, l := range out.Logs {
		val, err := ValidateLogValue(l.LogType, l.Value)
		if err != nil {
			continue
		}
		at := s.clampTime(l.RecordedAt, now)
		res.Logs = append(res.Logs, VoicePreviewLog{LogType: l.LogType, Value: val, RecordedAt: at})
	}
	for _, d := range out.Doses {
		m := medByID[d.MedicationID]
		if m == nil {
			continue // model menyebut obat yang tidak ada di profil
		}
		at := s.clampTime(d.GivenAt, now)
		slot, err := s.slotFor(ctx, m, at)
		if err != nil {
			return nil, err
		}
		c, err := s.checkConflict(ctx, m, slot, at)
		if err != nil {
			return nil, err
		}
		res.Doses = append(res.Doses, VoicePreviewDose{MedicationID: m.ID, MedicationName: m.Name, Kind: m.Kind, GivenAt: at, Conflict: c})
	}
	if len(res.Logs) == 0 && len(res.Doses) == 0 && res.UnmatchedText == "" {
		res.UnmatchedText = transcript
	}
	return res, nil
}

func (s *Service) clampTime(raw string, now time.Time) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil || t.After(now.Add(5*time.Minute)) || now.Sub(t) > 48*time.Hour {
		return now
	}
	return t.UTC()
}

// slotFor finds the scheduled slot of the same local day closest to `at` (within 3h).
func (s *Service) slotFor(ctx context.Context, m *db.Medication, at time.Time) (*db.Dose, error) {
	if m.IsPRN {
		return nil, nil
	}
	if err := s.generateFor(ctx, m, at); err != nil {
		return nil, err
	}
	from, to := DayBounds(at, s.Loc)
	doses, err := s.Store.DosesForProfile(ctx, m.CareProfileID, from, to)
	if err != nil {
		return nil, err
	}
	var best *db.Dose
	bestGap := 3 * time.Hour
	for i, d := range doses {
		if d.MedicationID != m.ID || d.ScheduledAt == nil {
			continue
		}
		gap := time.Duration(math.Abs(float64(d.ScheduledAt.Sub(at))))
		if gap <= bestGap {
			best, bestGap = &doses[i], gap
		}
	}
	return best, nil
}

type VoiceConfirmDose struct {
	MedicationID string  `json:"medication_id"`
	GivenAt      string  `json:"given_at"`
	Force        bool    `json:"force"`
	Reason       *string `json:"reason"`
}

type VoiceConfirmInput struct {
	Logs  []LogInput         `json:"logs"`
	Doses []VoiceConfirmDose `json:"doses"`
	Note  string             `json:"note"` // teks yang tidak dikenali, disimpan sebagai catatan
}

type VoiceConfirmResult struct {
	Logs  []db.CareLog `json:"logs"`
	Doses []db.Dose    `json:"doses"`
}

func (s *Service) VoiceConfirm(ctx context.Context, profileID, userID string, in VoiceConfirmInput) (*VoiceConfirmResult, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	// Validasi dan cek dosis ganda dulu untuk semua item, baru simpan.
	for _, l := range in.Logs {
		if _, err := ValidateLogValue(l.LogType, l.Value); err != nil {
			return nil, err
		}
	}
	type planned struct {
		med  *db.Medication
		slot *db.Dose
		at   time.Time
		c    *Conflict
		in   VoiceConfirmDose
	}
	var plan []planned
	for _, d := range in.Doses {
		m, err := s.Store.MedicationByID(ctx, d.MedicationID)
		if err != nil || m.CareProfileID != p.ID {
			return nil, apperr.Validation("Obat pada catatan suara tidak ditemukan di profil ini.")
		}
		at, err := s.parseGivenAt(&d.GivenAt)
		if err != nil {
			return nil, err
		}
		slot, err := s.slotFor(ctx, m, at)
		if err != nil {
			return nil, err
		}
		c, err := s.checkConflict(ctx, m, slot, at)
		if err != nil {
			return nil, err
		}
		if c != nil && !d.Force {
			return nil, s.doubleDoseError(m, c)
		}
		plan = append(plan, planned{med: m, slot: slot, at: at, c: c, in: d})
	}

	res := &VoiceConfirmResult{Logs: []db.CareLog{}, Doses: []db.Dose{}}
	for _, l := range in.Logs {
		saved, err := s.createLog(ctx, p, userID, l, "voice")
		if err != nil {
			return nil, err
		}
		res.Logs = append(res.Logs, *saved)
	}
	if note := strings.TrimSpace(in.Note); note != "" {
		v, _ := json.Marshal(map[string]string{"text": note})
		saved, err := s.createLog(ctx, p, userID, LogInput{LogType: "catatan", Value: v}, "voice")
		if err != nil {
			return nil, err
		}
		res.Logs = append(res.Logs, *saved)
	}
	for _, pl := range plan {
		var note *string
		if pl.c != nil {
			note = forcedNote(pl.in.Reason)
		}
		var id string
		if pl.slot != nil && pl.slot.Status != "given" {
			if err := s.Store.MarkDoseGiven(ctx, pl.slot.ID, pl.at, userID, note, pl.c != nil); err != nil {
				return nil, err
			}
			id = pl.slot.ID
		} else {
			if id, err = s.Store.InsertGivenDose(ctx, pl.med.ID, pl.at, userID, note, pl.c != nil); err != nil {
				return nil, err
			}
		}
		d, err := s.Store.DoseByID(ctx, id)
		if err != nil {
			return nil, err
		}
		res.Doses = append(res.Doses, *d)
		s.emit(p.FamilyID, "dose_given", p.ID, userID, d)
	}
	return res, nil
}

// ---------- Flow 3: trend analysis ----------

const trendThrottle = 10 * time.Minute

// maybeTrend runs Flow 3 in the background at most once per 10 minutes per profile.
func (s *Service) maybeTrend(p *db.Profile) {
	s.trendMu.Lock()
	last := s.trendLast[p.ID]
	if s.now().Sub(last) < trendThrottle {
		s.trendMu.Unlock()
		return
	}
	s.trendLast[p.ID] = s.now()
	s.trendMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := s.runTrend(ctx, p); err != nil {
			slog.Warn("trend otomatis gagal", "err", err)
		}
	}()
}

func (s *Service) Analyze(ctx context.Context, profileID, userID string) ([]db.Alert, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	s.trendMu.Lock()
	s.trendLast[p.ID] = s.now()
	s.trendMu.Unlock()
	created, err := s.runTrend(ctx, p)
	if err != nil {
		return nil, apperr.AIUnavailable("Analisis belum tersedia. Coba lagi nanti.")
	}
	return created, nil
}

func (s *Service) runTrend(ctx context.Context, p *db.Profile) ([]db.Alert, error) {
	now := s.now()
	from := now.AddDate(0, 0, -7)
	logs, err := s.Store.Logs(ctx, p.ID, from, now.Add(time.Second), "", 500)
	if err != nil {
		return nil, err
	}
	doses, err := s.Store.DosesForProfile(ctx, p.ID, from, now)
	if err != nil {
		return nil, err
	}
	in := langflow.TrendInput{ProfileType: p.ProfileType, ProfileName: p.DisplayName(), Now: s.localISO(now),
		Logs: s.logItems(logs), Doses: s.doseItems(doses)}
	var out langflow.TrendOutput
	if err := s.AI.Run(ctx, langflow.Trend, in, &out); err != nil {
		return nil, err
	}
	created := []db.Alert{}
	for _, a := range out.Alerts {
		if ContainsForbidden(a.Title + " " + a.Message) {
			slog.Warn("alert AI dibuang karena melanggar guardrail")
			continue
		}
		if saved := s.saveAlert(ctx, p, "ai", AlertDraft{Severity: a.Severity, Title: a.Title, Message: a.Message}); saved != nil {
			created = append(created, *saved)
		}
	}
	return created, nil
}

// ---------- Flow 4: doctor report ----------

type ReportInput struct {
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}

type ReportContent struct {
	langflow.ReportOutput
	AIGenerated bool   `json:"ai_generated"`
	ProfileName string `json:"profile_name"`
	ProfileType string `json:"profile_type"`
	Age         string `json:"age"`
	Notes       string `json:"notes"`
}

type SeriesPoint struct {
	At        time.Time `json:"at"`
	Value     float64   `json:"value"`
	Value2    float64   `json:"value2,omitempty"`
	AuthorRef string    `json:"id"`
}

type ReportView struct {
	*db.Report
	Content ReportContent            `json:"content"`
	Series  map[string][]SeriesPoint `json:"series"`
	Profile *db.Profile              `json:"profile"`
}

func (s *Service) parsePeriod(in ReportInput, p *db.Profile, ctx context.Context) (time.Time, time.Time, error) {
	now := s.now()
	end := now
	if in.PeriodEnd != "" {
		t, err := parseDateOrTime(in.PeriodEnd, s.Loc, true)
		if err != nil {
			return time.Time{}, time.Time{}, apperr.Validation("Tanggal akhir periode tidak valid.")
		}
		end = t
	}
	var start time.Time
	if in.PeriodStart != "" {
		t, err := parseDateOrTime(in.PeriodStart, s.Loc, false)
		if err != nil {
			return time.Time{}, time.Time{}, apperr.Validation("Tanggal awal periode tidak valid.")
		}
		start = t
	} else {
		start = end.AddDate(0, 0, -7)
		if last, err := s.Store.LatestReport(ctx, p.ID); err == nil && last != nil && last.PeriodEnd.After(start) {
			start = last.PeriodEnd
		}
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, apperr.Validation("Awal periode harus sebelum akhir periode.")
	}
	if end.Sub(start) > 62*24*time.Hour {
		return time.Time{}, time.Time{}, apperr.Validation("Periode laporan maksimal 2 bulan.")
	}
	return start.UTC(), end.UTC(), nil
}

func parseDateOrTime(s string, loc *time.Location, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return t, err
	}
	if endOfDay {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

func ageLabel(birth *string, now time.Time) string {
	if birth == nil {
		return ""
	}
	b, err := time.Parse("2006-01-02", *birth)
	if err != nil {
		return ""
	}
	years := now.Year() - b.Year()
	if now.YearDay() < b.YearDay() {
		years--
	}
	if years < 2 {
		months := int(now.Sub(b).Hours() / 24 / 30.4)
		return fmt.Sprintf("%d bulan", months)
	}
	return fmt.Sprintf("%d tahun", years)
}

func (s *Service) CreateReport(ctx context.Context, profileID, userID string, in ReportInput) (*ReportView, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	start, end, err := s.parsePeriod(in, p, ctx)
	if err != nil {
		return nil, err
	}
	logs, err := s.Store.Logs(ctx, p.ID, start, end, "", 2000)
	if err != nil {
		return nil, err
	}
	doses, err := s.Store.DosesForProfile(ctx, p.ID, start, end)
	if err != nil {
		return nil, err
	}
	alerts, err := s.Store.Alerts(ctx, p.ID, false, 100)
	if err != nil {
		return nil, err
	}
	var periodAlerts []db.Alert
	for _, a := range alerts {
		if !a.CreatedAt.Before(start) && a.CreatedAt.Before(end) {
			periodAlerts = append(periodAlerts, a)
		}
	}
	now := s.now()
	content := ReportContent{ProfileName: p.DisplayName(), ProfileType: p.ProfileType, Age: ageLabel(p.BirthDate, now), Notes: deref(p.Notes, "")}
	aiIn := langflow.ReportInput{ProfileType: p.ProfileType, ProfileName: p.DisplayName(), Age: content.Age, Notes: content.Notes,
		PeriodStart: s.localISO(start), PeriodEnd: s.localISO(end), Logs: s.logItems(logs), Doses: s.doseItems(doses), Alerts: alertItems(periodAlerts)}
	var out langflow.ReportOutput
	if err := s.AI.Run(ctx, langflow.DoctorReport, aiIn, &out); err != nil || ContainsForbidden(out.Overview+" "+strings.Join(out.QuestionsForDoctor, " ")) {
		s.logErr("doctor report flow", err)
		out = langflow.ReportOutput{Overview: "Ringkasan otomatis belum tersedia. Data catatan di bawah tetap lengkap.",
			KeyObservations: []string{}, Symptoms: symptomsFrom(logs), QuestionsForDoctor: []string{}}
	} else {
		content.AIGenerated = true
	}
	// Kepatuhan obat dihitung pasti dari data, bukan dari model.
	out.MedicationAdherence = adherence(doses, now)
	content.ReportOutput = out

	raw, _ := json.Marshal(content)
	uid := userID
	r := &db.Report{CareProfileID: p.ID, PeriodStart: start, PeriodEnd: end, Content: raw, CreatedBy: &uid}
	if err := s.Store.CreateReport(ctx, r); err != nil {
		return nil, err
	}
	return s.reportView(ctx, r, p)
}

func symptomsFrom(logs []db.CareLog) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, l := range logs {
		if l.LogType != "keluhan" {
			continue
		}
		var v struct{ Text string }
		_ = json.Unmarshal(l.Value, &v)
		if t := strings.TrimSpace(v.Text); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func adherence(doses []db.Dose, now time.Time) []langflow.Adherence {
	idx := map[string]int{}
	out := []langflow.Adherence{}
	for _, d := range doses {
		i, ok := idx[d.MedicationID]
		if !ok {
			i = len(out)
			idx[d.MedicationID] = i
			out = append(out, langflow.Adherence{Name: d.MedicationName})
		}
		switch {
		case d.Status == "given":
			out[i].Given++
		case d.Status == "skipped" || IsMissed(d.Status, d.ScheduledAt, now):
			out[i].Missed++
		}
	}
	return out
}

func (s *Service) Report(ctx context.Context, reportID, userID string) (*ReportView, error) {
	r, err := s.Store.ReportByID(ctx, reportID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apperr.NotFound("Laporan tidak ditemukan.")
	}
	if err != nil {
		return nil, err
	}
	p, err := s.RequireProfile(ctx, r.CareProfileID, userID)
	if err != nil {
		return nil, err
	}
	return s.reportView(ctx, r, p)
}

func (s *Service) LatestReport(ctx context.Context, profileID, userID string) (*ReportView, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	r, err := s.Store.LatestReport(ctx, p.ID)
	if err != nil || r == nil {
		return nil, err
	}
	return s.reportView(ctx, r, p)
}

// reportView attaches raw chart series; charts are drawn from data, never by the model.
func (s *Service) reportView(ctx context.Context, r *db.Report, p *db.Profile) (*ReportView, error) {
	v := &ReportView{Report: r, Profile: p, Series: map[string][]SeriesPoint{}}
	_ = json.Unmarshal(r.Content, &v.Content)
	logs, err := s.Store.Logs(ctx, p.ID, r.PeriodStart, r.PeriodEnd, "", 2000)
	if err != nil {
		return nil, err
	}
	for i := len(logs) - 1; i >= 0; i-- { // urut naik waktu
		l := logs[i]
		var val struct {
			Celsius             float64
			Systolic, Diastolic float64
			MgDl                float64 `json:"mg_dl"`
		}
		_ = json.Unmarshal(l.Value, &val)
		switch l.LogType {
		case "suhu":
			v.Series["suhu"] = append(v.Series["suhu"], SeriesPoint{At: l.RecordedAt, Value: val.Celsius, AuthorRef: l.ID})
		case "tensi":
			v.Series["tensi"] = append(v.Series["tensi"], SeriesPoint{At: l.RecordedAt, Value: val.Systolic, Value2: val.Diastolic, AuthorRef: l.ID})
		case "gula_darah":
			v.Series["gula_darah"] = append(v.Series["gula_darah"], SeriesPoint{At: l.RecordedAt, Value: val.MgDl, AuthorRef: l.ID})
		}
	}
	return v, nil
}

// ---------- Flow 5: ask ----------

type AskSource struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"` // log | dose
	Label string    `json:"label"`
	At    time.Time `json:"at"`
}

type AskResult struct {
	Question string      `json:"question"`
	Answer   string      `json:"answer"`
	Sources  []AskSource `json:"sources"`
}

const toolSessionTTL = 10 * time.Minute

// newToolSession returns a short random id that lets the agent read ONE profile for 10 minutes.
func (s *Service) newToolSession(profileID string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	now := s.now()
	for k, v := range s.toolSessions {
		if now.After(v.Expires) {
			delete(s.toolSessions, k)
		}
	}
	s.toolSessions[id] = toolSession{ProfileID: profileID, Expires: now.Add(toolSessionTTL)}
	return id
}

// ToolSessionAllows reports whether an internal-tool call may read the profile.
func (s *Service) ToolSessionAllows(session, profileID string) bool {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	ts, ok := s.toolSessions[session]
	return ok && ts.ProfileID == profileID && s.now().Before(ts.Expires)
}

func (s *Service) Ask(ctx context.Context, profileID, userID, question string) (*AskResult, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, apperr.Validation("Tulis pertanyaannya dulu.")
	}
	if len([]rune(question)) > 300 {
		return nil, apperr.Validation("Pertanyaan terlalu panjang. Maksimal 300 karakter.")
	}
	now := s.now()
	base := langflow.AskInput{CareProfileID: p.ID, ProfileName: p.DisplayName(), Question: question, Now: s.localISO(now),
		ToolBaseURL: s.PublicBaseURL + "/internal/tools", ToolToken: s.newToolSession(p.ID)}

	// Data 14 hari terakhir: konteks untuk mock, dan untuk memvalidasi sumber jawaban.
	from := now.AddDate(0, 0, -14)
	logs, err := s.Store.Logs(ctx, p.ID, from, now.Add(time.Second), "", 1000)
	if err != nil {
		return nil, err
	}
	doses, err := s.Store.DosesForProfile(ctx, p.ID, from, now.Add(time.Second))
	if err != nil {
		return nil, err
	}

	// Jawaban lokal dari data: dipakai di mode mock, dan sebagai cadangan bila agent gagal.
	local := func() (string, error) {
		runner := s.AI
		if !runner.Mock() {
			runner = s.Fallback
		}
		if runner == nil {
			return "", errors.New("tidak ada runner cadangan")
		}
		in := langflow.MockAskInput{AskInput: base}
		in.Context.Logs, in.Context.Doses = s.logItems(logs), s.doseItems(doses)
		var o langflow.AskOutput
		if err := runner.Run(ctx, langflow.AskHistory, in, &o); err != nil {
			return "", err
		}
		b, _ := json.Marshal(o)
		return string(b), nil
	}
	var text string
	if s.AI.Mock() {
		text, err = local()
	} else if err = s.runInteractive(ctx, langflow.AskHistory, base, &text); err != nil || agentCouldNotFetch(text) {
		slog.Warn("flow ask_history gagal, memakai jawaban lokal", "err", err)
		text, err = local()
	}
	if err != nil {
		return nil, apperr.AIUnavailable("Tanya belum bisa menjawab sekarang. Coba lagi sebentar lagi.")
	}
	o := langflow.ParseAsk(text)
	if ContainsForbidden(o.Answer) {
		o.Answer = "Pertanyaan ini sebaiknya ditanyakan langsung ke dokter atau tenaga kesehatan. Operan hanya bisa menjawab berdasarkan catatan yang ada."
		o.Sources = nil
	}

	res := &AskResult{Question: question, Answer: o.Answer, Sources: []AskSource{}}
	logByID := map[string]db.CareLog{}
	for _, l := range logs {
		logByID[l.ID] = l
	}
	doseByID := map[string]db.Dose{}
	for _, d := range doses {
		doseByID[d.ID] = d
	}
	for _, id := range o.Sources {
		if l, ok := logByID[id]; ok {
			label, _, _ := describeLog(l, s.Loc)
			res.Sources = append(res.Sources, AskSource{ID: id, Kind: "log", Label: label, At: l.RecordedAt})
		} else if d, ok := doseByID[id]; ok && d.GivenAt != nil {
			res.Sources = append(res.Sources, AskSource{ID: id, Kind: "dose", At: *d.GivenAt,
				Label: fmt.Sprintf("%s, diberikan %s pukul %s", d.MedicationName, deref(d.GivenByName, "anggota keluarga"), d.GivenAt.In(s.Loc).Format("15.04"))})
		}
	}
	return res, nil
}

// ---------- internal tools for the Flow 5 agent ----------

func (s *Service) ToolLogs(ctx context.Context, profileID string, from, to time.Time, logType string) ([]langflow.LogItem, error) {
	logs, err := s.Store.Logs(ctx, profileID, from, to, logType, 300)
	if err != nil {
		return nil, err
	}
	return s.logItems(logs), nil
}

func (s *Service) ToolDoses(ctx context.Context, profileID string, from, to time.Time) ([]langflow.DoseItem, error) {
	doses, err := s.Store.DosesForProfile(ctx, profileID, from, to)
	if err != nil {
		return nil, err
	}
	return s.doseItems(doses), nil
}

func (s *Service) ToolMedications(ctx context.Context, profileID string) ([]db.Medication, error) {
	return s.Store.MedicationsByProfile(ctx, profileID, false)
}

// agentCouldNotFetch detects the agent's "tool failed" answer (see flows/prompts/05-ask-history.md).
func agentCouldNotFetch(text string) bool {
	return strings.Contains(strings.ToLower(text), "belum bisa diambil")
}

// interactiveAIBudget caps how long someone waits on the voice and Ask screens before
// the deterministic fallback answers instead (e.g. when the model is rate limited).
const interactiveAIBudget = 25 * time.Second

func (s *Service) runInteractive(ctx context.Context, flow langflow.FlowName, payload any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, interactiveAIBudget)
	defer cancel()
	return s.AI.Run(ctx, flow, payload, out)
}
