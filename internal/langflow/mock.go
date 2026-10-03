package langflow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MockRunner produces realistic, deterministic flow output derived from the payload,
// so every AI screen works before the real Langflow flows exist (LANGFLOW_MOCK=true).
type MockRunner struct {
	Loc   *time.Location
	Delay time.Duration // simulate model latency in the UI
}

func NewMockRunner(loc *time.Location) *MockRunner {
	return &MockRunner{Loc: loc, Delay: 600 * time.Millisecond}
}

func (m *MockRunner) Mock() bool { return true }

func (m *MockRunner) Run(ctx context.Context, flow FlowName, payload any, out any) error {
	if m.Delay > 0 {
		select {
		case <-time.After(m.Delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var result any
	switch flow {
	case ExtractVoice:
		var in VoiceInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		result = m.extract(in)
	case Handover:
		var in HandoverInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		result = m.handover(in)
	case Trend:
		var in TrendInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		result = m.trend(in)
	case DoctorReport:
		var in ReportInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		result = m.report(in)
	case AskHistory:
		var in MockAskInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		result = m.ask(in)
	default:
		return fmt.Errorf("mock: flow %s tidak dikenal", flow)
	}
	text, _ := json.Marshal(result)
	// Lewati jalur parser yang sama dengan HTTPRunner (termasuk code fence).
	return DecodeOutput("```json\n"+string(text)+"\n```", out)
}

// ---------- helpers ----------

func (m *MockRunner) parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.In(m.Loc), true
}

func (m *MockRunner) clock(s string) string {
	if t, ok := m.parseTime(s); ok {
		return t.Format("15.04")
	}
	return ""
}

func dec(v float64) string {
	return strings.Replace(strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64), ".", ",", 1)
}

type numVal struct {
	Celsius   float64 `json:"celsius"`
	Systolic  int     `json:"systolic"`
	Diastolic int     `json:"diastolic"`
	MgDl      float64 `json:"mg_dl"`
	Portion   string  `json:"portion"`
	Quality   string  `json:"quality"`
	Hours     float64 `json:"hours"`
	Text      string  `json:"text"`
	Note      string  `json:"note"`
}

func valOf(l LogItem) numVal {
	var v numVal
	_ = json.Unmarshal(l.Value, &v)
	return v
}

var portionText = map[string]string{"habis": "habis", "setengah": "setengah porsi", "sedikit": "sedikit", "tidak_mau": "tidak mau makan"}

// ---------- Flow 1 ----------

var (
	reTemp    = regexp.MustCompile(`(?:suhu\w*|panas\w*|demam\w*)\D{0,12}(\d{2})(?:[,.](\d))?`)
	reTempAlt = regexp.MustCompile(`\b(3[4-9]|4[0-3])[,.](\d)\b`)
	reBP      = regexp.MustCompile(`(\d{2,3})\s*(?:/|per)\s*(\d{2,3})`)
	reSugar   = regexp.MustCompile(`gula\w*(?:\s+darah\w*)?\D{0,10}(\d{2,3})`)
	reClock   = regexp.MustCompile(`jam\s+(\d{1,2})(?:[.:](\d{2}))?(?:\s+(pagi|siang|sore|malam))?`)
	reMeal    = regexp.MustCompile(`makan\w*\s+(?:\w+\s+)?(habis|setengah|sedikit|tidak mau|gak mau|nggak mau)`)
)

var complaintWords = []string{"batuk", "pilek", "muntah", "mual", "diare", "pusing", "rewel", "sesak", "nyeri", "lemas", "gatal", "ruam", "sakit kepala", "sakit perut"}

func (m *MockRunner) extract(in VoiceInput) VoiceOutput {
	now, ok := m.parseTime(in.Now)
	if !ok {
		now = time.Now().In(m.Loc)
	}
	nowStr := now.Format(time.RFC3339)
	t := strings.ToLower(in.Transcript)
	out := VoiceOutput{Logs: []ExtractedLog{}, Doses: []ExtractedDose{}}
	matched := false

	if mm := reBP.FindStringSubmatch(t); mm != nil {
		sys, _ := strconv.Atoi(mm[1])
		dia, _ := strconv.Atoi(mm[2])
		if sys >= 60 && sys <= 260 && dia >= 30 && dia <= 160 {
			v, _ := json.Marshal(map[string]int{"systolic": sys, "diastolic": dia})
			out.Logs = append(out.Logs, ExtractedLog{LogType: "tensi", Value: v, RecordedAt: nowStr})
			matched = true
			t = strings.Replace(t, mm[0], " ", 1)
		}
	}
	var temp float64
	if mm := reTemp.FindStringSubmatch(t); mm != nil {
		temp, _ = strconv.ParseFloat(mm[1]+"."+orZero(mm[2]), 64)
	} else if mm := reTempAlt.FindStringSubmatch(t); mm != nil {
		temp, _ = strconv.ParseFloat(mm[1]+"."+mm[2], 64)
	}
	if temp >= 34 && temp <= 43 {
		v, _ := json.Marshal(map[string]float64{"celsius": temp})
		out.Logs = append(out.Logs, ExtractedLog{LogType: "suhu", Value: v, RecordedAt: nowStr})
		matched = true
	}
	if mm := reSugar.FindStringSubmatch(t); mm != nil {
		if v, _ := strconv.Atoi(mm[1]); v >= 20 && v <= 600 {
			b, _ := json.Marshal(map[string]any{"mg_dl": v, "context": "sewaktu"})
			out.Logs = append(out.Logs, ExtractedLog{LogType: "gula_darah", Value: b, RecordedAt: nowStr})
			matched = true
		}
	}
	if mm := reMeal.FindStringSubmatch(t); mm != nil {
		p := mm[1]
		if strings.Contains(p, "mau") {
			p = "tidak_mau"
		}
		b, _ := json.Marshal(map[string]string{"portion": p, "note": ""})
		out.Logs = append(out.Logs, ExtractedLog{LogType: "makan", Value: b, RecordedAt: nowStr})
		matched = true
	}
	var complaints []string
	for _, w := range complaintWords {
		if strings.Contains(t, w) {
			complaints = append(complaints, w)
		}
	}
	if len(complaints) > 0 {
		b, _ := json.Marshal(map[string]string{"text": strings.Join(complaints, ", ")})
		out.Logs = append(out.Logs, ExtractedLog{LogType: "keluhan", Value: b, RecordedAt: nowStr})
		matched = true
	}

	// Dosis: cocokkan nama obat atau sebutan umum ("penurun panas", "obat tensi", "vitamin").
	givenWords := []string{"sudah", "minum", "diberi", "dikasih", "dikasi", "kasih", "diminum"}
	if containsAny(t, givenWords) {
		at := now
		if mm := reClock.FindStringSubmatch(t); mm != nil {
			h, _ := strconv.Atoi(mm[1])
			mi, _ := strconv.Atoi(orZero(mm[2]))
			if (mm[3] == "sore" || mm[3] == "malam") && h < 12 {
				h += 12
			}
			cand := time.Date(now.Year(), now.Month(), now.Day(), h%24, mi, 0, 0, m.Loc)
			if cand.After(now.Add(5 * time.Minute)) {
				cand = cand.AddDate(0, 0, -1)
			}
			at = cand
		}
		for _, med := range in.Medications {
			if medMentioned(t, med) {
				out.Doses = append(out.Doses, ExtractedDose{MedicationID: med.ID, GivenAt: at.Format(time.RFC3339)})
				matched = true
			}
		}
	}
	if !matched {
		out.UnmatchedText = strings.TrimSpace(in.Transcript)
	}
	return out
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func medMentioned(t string, med VoiceMed) bool {
	name := strings.ToLower(med.Name)
	first := strings.Fields(name)
	if len(first) > 0 && len(first[0]) >= 4 && strings.Contains(t, first[0]) {
		return true
	}
	aliases := map[string][]string{
		"penurun panas": {"paracetamol", "parasetamol", "ibuprofen", "penurun panas", "sanmol", "tempra", "proris"},
		"obat tensi":    {"amlodipin", "amlodipine", "captopril", "candesartan", "tensi", "hipertensi"},
		"obat gula":     {"metformin", "glimepiride", "gula", "insulin"},
	}
	for phrase, keys := range aliases {
		if strings.Contains(t, phrase) && containsAny(name, keys) {
			return true
		}
	}
	return med.Kind == "vitamin" && strings.Contains(t, "vitamin")
}

// ---------- Flow 2 ----------

func (m *MockRunner) handover(in HandoverInput) HandoverOutput {
	logs := append([]LogItem(nil), in.Logs...)
	sort.Slice(logs, func(i, j int) bool { return logs[i].RecordedAt < logs[j].RecordedAt })
	var parts, watch []string

	var temps []LogItem
	var lastBP, lastMeal, lastSleep *LogItem
	var complaints []string
	for i := range logs {
		switch logs[i].Type {
		case "suhu":
			temps = append(temps, logs[i])
		case "tensi":
			lastBP = &logs[i]
		case "makan":
			lastMeal = &logs[i]
		case "tidur":
			lastSleep = &logs[i]
		case "keluhan", "catatan":
			if tx := valOf(logs[i]).Text; tx != "" {
				complaints = append(complaints, tx)
			}
		}
	}
	if len(temps) > 0 {
		maxT := temps[0]
		for _, l := range temps {
			if valOf(l).Celsius > valOf(maxT).Celsius {
				maxT = l
			}
		}
		last := temps[len(temps)-1]
		if maxT.RecordedAt != last.RecordedAt && valOf(maxT).Celsius > valOf(last).Celsius {
			parts = append(parts, fmt.Sprintf("Suhu sempat %s° pukul %s, terakhir %s° pukul %s.",
				dec(valOf(maxT).Celsius), m.clock(maxT.RecordedAt), dec(valOf(last).Celsius), m.clock(last.RecordedAt)))
		} else {
			parts = append(parts, fmt.Sprintf("Suhu terakhir %s° pukul %s.", dec(valOf(last).Celsius), m.clock(last.RecordedAt)))
		}
		if valOf(last).Celsius >= 38 {
			watch = append(watch, "Suhu masih di atas 38°")
		}
	}
	if lastBP != nil {
		v := valOf(*lastBP)
		parts = append(parts, fmt.Sprintf("Tensi terakhir %d/%d pukul %s.", v.Systolic, v.Diastolic, m.clock(lastBP.RecordedAt)))
		if v.Systolic >= 160 {
			watch = append(watch, fmt.Sprintf("Tensi masih tinggi (%d/%d)", v.Systolic, v.Diastolic))
		}
	}
	if lastMeal != nil {
		parts = append(parts, fmt.Sprintf("Makan %s.", portionText[valOf(*lastMeal).Portion]))
	}
	if lastSleep != nil {
		v := valOf(*lastSleep)
		q := map[string]string{"nyenyak": "nyenyak", "gelisah": "gelisah", "sulit": "sulit tidur"}[v.Quality]
		if q != "" {
			parts = append(parts, fmt.Sprintf("Tidur %s.", q))
		}
	}
	if len(complaints) > 0 {
		parts = append(parts, "Keluhan: "+strings.Join(uniq(complaints), ", ")+".")
	}

	var given []string
	var pending []string
	for _, d := range in.Doses {
		switch d.Status {
		case "given":
			given = append(given, fmt.Sprintf("%s %s", d.Medication, m.clock(d.GivenAt)))
		case "pending", "missed":
			pending = append(pending, fmt.Sprintf("%s %s belum diberikan", d.Medication, m.clock(d.ScheduledAt)))
		}
	}
	if len(given) > 0 {
		parts = append(parts, "Sudah diberikan: "+strings.Join(given, ", ")+".")
	}
	for _, a := range in.Alerts {
		if a.Severity == "penting" {
			watch = append(watch, a.Title)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("Belum ada catatan untuk %s selama giliran ini.", in.ProfileName))
	}
	if pending == nil {
		pending = []string{}
	}
	if watch == nil {
		watch = []string{}
	}
	return HandoverOutput{Summary: strings.Join(parts, " "), PendingItems: pending, WatchItems: uniq(watch)}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------- Flow 3 ----------

func (m *MockRunner) trend(in TrendInput) TrendOutput {
	out := TrendOutput{Alerts: []TrendAlert{}}
	complaintDays := map[string]map[string]bool{}
	poorMeals := 0
	for _, l := range in.Logs {
		t, ok := m.parseTime(l.RecordedAt)
		if !ok {
			continue
		}
		day := t.Format("2006-01-02")
		v := valOf(l)
		switch l.Type {
		case "keluhan":
			for _, w := range complaintWords {
				if strings.Contains(strings.ToLower(v.Text), w) {
					if complaintDays[w] == nil {
						complaintDays[w] = map[string]bool{}
					}
					complaintDays[w][day] = true
				}
			}
		case "makan":
			if v.Portion == "sedikit" || v.Portion == "tidak_mau" {
				poorMeals++
			}
		}
	}
	keys := make([]string, 0, len(complaintDays))
	for k := range complaintDays {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, w := range keys {
		if n := len(complaintDays[w]); n >= 3 {
			out.Alerts = append(out.Alerts, TrendAlert{Severity: "info",
				Title:   fmt.Sprintf("Keluhan %s tercatat %d hari", w, n),
				Message: fmt.Sprintf("Keluhan %s pada %s tercatat di %d hari berbeda minggu ini. Sampaikan ke dokter saat kontrol, atau hubungi tenaga kesehatan bila memberat.", w, in.ProfileName, n)})
		}
	}
	if poorMeals >= 3 {
		out.Alerts = append(out.Alerts, TrendAlert{Severity: "perhatian",
			Title:   fmt.Sprintf("Nafsu makan %s menurun", in.ProfileName),
			Message: fmt.Sprintf("%s tercatat makan sedikit atau tidak mau makan %d kali dalam seminggu terakhir. Pertimbangkan untuk berkonsultasi dengan tenaga kesehatan bila berlanjut.", in.ProfileName, poorMeals)})
	}
	return out
}

// ---------- Flow 4 ----------

func (m *MockRunner) report(in ReportInput) ReportOutput {
	out := ReportOutput{MedicationAdherence: []Adherence{}, KeyObservations: []string{}, Symptoms: []string{}, QuestionsForDoctor: []string{}}
	var maxTemp *LogItem
	feverDays := map[string]bool{}
	var sysVals []int
	var symptoms []string
	poorMeals := 0
	for i, l := range in.Logs {
		v := valOf(l)
		switch l.Type {
		case "suhu":
			if maxTemp == nil || v.Celsius > valOf(*maxTemp).Celsius {
				maxTemp = &in.Logs[i]
			}
			if v.Celsius >= 38 {
				if t, ok := m.parseTime(l.RecordedAt); ok {
					feverDays[t.Format("2006-01-02")] = true
				}
			}
		case "tensi":
			sysVals = append(sysVals, v.Systolic)
		case "keluhan":
			if v.Text != "" {
				symptoms = append(symptoms, v.Text)
			}
		case "makan":
			if v.Portion == "sedikit" || v.Portion == "tidak_mau" {
				poorMeals++
			}
		}
	}
	var overview []string
	if maxTemp != nil {
		t, _ := m.parseTime(maxTemp.RecordedAt)
		if len(feverDays) > 0 {
			overview = append(overview, fmt.Sprintf("Suhu 38° atau lebih tercatat di %d hari, tertinggi %s° pada %d/%d pukul %s.",
				len(feverDays), dec(valOf(*maxTemp).Celsius), t.Day(), int(t.Month()), t.Format("15.04")))
			out.KeyObservations = append(out.KeyObservations, fmt.Sprintf("Suhu tertinggi %s° (%d/%d %s)", dec(valOf(*maxTemp).Celsius), t.Day(), int(t.Month()), t.Format("15.04")))
		} else {
			overview = append(overview, fmt.Sprintf("Suhu tercatat di bawah 38° selama periode ini, tertinggi %s°.", dec(valOf(*maxTemp).Celsius)))
		}
	}
	if len(sysVals) > 0 {
		lo, hi := sysVals[0], sysVals[0]
		for _, s := range sysVals {
			lo, hi = min(lo, s), max(hi, s)
		}
		overview = append(overview, fmt.Sprintf("Tensi atas tercatat %d kali, antara %d dan %d.", len(sysVals), lo, hi))
		out.KeyObservations = append(out.KeyObservations, fmt.Sprintf("Tensi atas %d–%d dari %d pengukuran", lo, hi, len(sysVals)))
	}
	if poorMeals > 0 {
		out.KeyObservations = append(out.KeyObservations, fmt.Sprintf("Makan sedikit atau menolak makan %d kali", poorMeals))
	}
	given, missed := 0, 0
	for _, d := range in.Doses {
		if d.Status == "given" {
			given++
		} else if d.Status == "missed" {
			missed++
		}
	}
	overview = append(overview, fmt.Sprintf("Obat dan vitamin diberikan %d kali, %d jadwal terlewat.", given, missed))
	out.Overview = strings.Join(overview, " ")
	out.Symptoms = uniq(symptoms)

	switch in.ProfileType {
	case "anak", "pemulihan":
		if len(feverDays) > 0 {
			out.QuestionsForDoctor = append(out.QuestionsForDoctor,
				"Tanda apa saja yang membuat kami perlu segera kembali periksa?",
				"Berapa lama lagi suhu perlu dipantau di rumah?")
		}
	case "lansia", "kronis":
		if len(sysVals) > 0 {
			out.QuestionsForDoctor = append(out.QuestionsForDoctor,
				"Apakah hasil tensi selama periode ini sudah sesuai target?",
				"Seberapa sering tensi sebaiknya diukur di rumah?")
		}
	}
	if len(out.Symptoms) > 0 {
		out.QuestionsForDoctor = append(out.QuestionsForDoctor, "Apakah keluhan yang dicatat perlu pemeriksaan tambahan?")
	}
	if missed > 0 {
		out.QuestionsForDoctor = append(out.QuestionsForDoctor, "Apa yang sebaiknya dilakukan jika jadwal obat terlewat?")
	}
	return out
}

// ---------- Flow 5 ----------

// MockAskInput is AskInput plus a data snapshot; only the mock needs the snapshot,
// the real agent fetches data through the internal tool endpoints.
type MockAskInput struct {
	AskInput
	Context struct {
		Logs  []LogItem  `json:"logs"`
		Doses []DoseItem `json:"doses"`
	} `json:"context"`
}

func (m *MockRunner) ask(in MockAskInput) AskOutput {
	now, ok := m.parseTime(in.Now)
	if !ok {
		now = time.Now().In(m.Loc)
	}
	q := strings.ToLower(in.Question)
	name := in.ProfileName

	logsOf := func(typ string) []LogItem {
		var out []LogItem
		for _, l := range in.Context.Logs {
			if l.Type == typ {
				out = append(out, l)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].RecordedAt > out[j].RecordedAt })
		return out
	}
	when := func(s string) string {
		t, ok := m.parseTime(s)
		if !ok {
			return s
		}
		return relDay(t, now) + " pukul " + t.Format("15.04")
	}
	by := func(s string) string {
		if s == "" {
			return ""
		}
		return ", dicatat " + s
	}

	switch {
	case containsAny(q, []string{"obat", "minum", "diberi", "vitamin", "penurun panas", "dosis"}):
		var best *DoseItem
		for i, d := range in.Context.Doses {
			if d.Status != "given" {
				continue
			}
			if !doseMatchesQuestion(q, d) {
				continue
			}
			if best == nil || d.GivenAt > best.GivenAt {
				best = &in.Context.Doses[i]
			}
		}
		if best == nil {
			return AskOutput{Answer: fmt.Sprintf("Belum ada catatan pemberian obat itu untuk %s.", name), Sources: []string{}}
		}
		giver := ""
		if best.By != "" {
			giver = " oleh " + best.By
		}
		return AskOutput{Answer: fmt.Sprintf("%s terakhir diberikan%s %s.", best.Medication, giver, when(best.GivenAt)), Sources: []string{best.ID}}
	case strings.Contains(q, "suhu") || strings.Contains(q, "demam") || strings.Contains(q, "panas"):
		temps := logsOf("suhu")
		if len(temps) == 0 {
			return AskOutput{Answer: fmt.Sprintf("Belum ada catatan suhu untuk %s.", name), Sources: []string{}}
		}
		if strings.Contains(q, "tertinggi") || strings.Contains(q, "paling tinggi") {
			hi := temps[0]
			for _, l := range temps {
				if valOf(l).Celsius > valOf(hi).Celsius {
					hi = l
				}
			}
			return AskOutput{Answer: fmt.Sprintf("Suhu tertinggi %s° %s%s.", dec(valOf(hi).Celsius), when(hi.RecordedAt), by(hi.By)), Sources: []string{hi.ID}}
		}
		l := temps[0]
		return AskOutput{Answer: fmt.Sprintf("Suhu terakhir %s° %s%s.", dec(valOf(l).Celsius), when(l.RecordedAt), by(l.By)), Sources: []string{l.ID}}
	case strings.Contains(q, "tensi") || strings.Contains(q, "tekanan darah"):
		bps := logsOf("tensi")
		if len(bps) == 0 {
			return AskOutput{Answer: fmt.Sprintf("Belum ada catatan tensi untuk %s.", name), Sources: []string{}}
		}
		l := bps[0]
		v := valOf(l)
		return AskOutput{Answer: fmt.Sprintf("Tensi terakhir %d/%d %s%s.", v.Systolic, v.Diastolic, when(l.RecordedAt), by(l.By)), Sources: []string{l.ID}}
	case strings.Contains(q, "keluhan") || strings.Contains(q, "gejala"):
		ks := logsOf("keluhan")
		if len(ks) == 0 {
			return AskOutput{Answer: fmt.Sprintf("Belum ada keluhan yang dicatat untuk %s.", name), Sources: []string{}}
		}
		var parts []string
		var src []string
		for _, l := range ks {
			if len(parts) >= 4 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", valOf(l).Text, when(l.RecordedAt)))
			src = append(src, l.ID)
		}
		return AskOutput{Answer: "Keluhan yang dicatat: " + strings.Join(parts, "; ") + ".", Sources: src}
	case strings.Contains(q, "makan"):
		ms := logsOf("makan")
		if len(ms) == 0 {
			return AskOutput{Answer: fmt.Sprintf("Belum ada catatan makan untuk %s.", name), Sources: []string{}}
		}
		l := ms[0]
		return AskOutput{Answer: fmt.Sprintf("Terakhir makan %s %s%s.", portionText[valOf(l).Portion], when(l.RecordedAt), by(l.By)), Sources: []string{l.ID}}
	case strings.Contains(q, "tidur"):
		ss := logsOf("tidur")
		if len(ss) == 0 {
			return AskOutput{Answer: fmt.Sprintf("Belum ada catatan tidur untuk %s.", name), Sources: []string{}}
		}
		l := ss[0]
		v := valOf(l)
		return AskOutput{Answer: fmt.Sprintf("Catatan tidur terakhir: %s, sekitar %s jam (%s).", v.Quality, dec(v.Hours), when(l.RecordedAt)), Sources: []string{l.ID}}
	}
	return AskOutput{Answer: "Belum ada catatan yang bisa menjawab pertanyaan itu. Coba tanyakan tentang obat, suhu, tensi, makan, atau keluhan.", Sources: []string{}}
}

func doseMatchesQuestion(q string, d DoseItem) bool {
	name := strings.ToLower(d.Medication)
	if strings.Contains(q, "penurun panas") {
		return containsAny(name, []string{"paracetamol", "parasetamol", "ibuprofen", "penurun panas", "sanmol", "tempra"})
	}
	if strings.Contains(q, "vitamin") {
		return d.Kind == "vitamin" || strings.Contains(name, "vitamin")
	}
	if strings.Contains(q, "tensi") {
		return containsAny(name, []string{"amlodipin", "captopril", "candesartan", "tensi"})
	}
	for _, w := range strings.Fields(name) {
		if len(w) >= 4 && strings.Contains(q, w) {
			return true
		}
	}
	// Pertanyaan umum ("obat terakhir") cocok dengan semua obat.
	return !containsAny(q, []string{"paracetamol", "ibuprofen", "amlodipin"})
}

func relDay(t, now time.Time) string {
	ty, tm, td := t.Date()
	ny, nm, nd := now.Date()
	tDay := time.Date(ty, tm, td, 0, 0, 0, 0, t.Location())
	nDay := time.Date(ny, nm, nd, 0, 0, 0, 0, now.Location())
	switch int(nDay.Sub(tDay).Hours() / 24) {
	case 0:
		return "hari ini"
	case 1:
		return "kemarin"
	}
	months := []string{"", "Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	return fmt.Sprintf("%d %s", td, months[tm])
}
