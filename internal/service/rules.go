// Package service holds business logic. The functions in this file are pure and
// deterministic: medication safety must never depend on the LLM.
package service

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- double dose ----------

// GivenRecord is one past administration of the same medication.
type GivenRecord struct {
	At     time.Time
	ByName string
}

// Conflict explains why a dose is held back. Field names match the 409 details.
type Conflict struct {
	Reason             string     `json:"reason"` // "interval" | "slot"
	LastGivenAt        time.Time  `json:"last_given_at"`
	LastGivenByName    string     `json:"last_given_by_name"`
	MinIntervalMinutes *int       `json:"min_interval_minutes"`
	AllowedFrom        *time.Time `json:"allowed_from"`
}

// CheckDoubleDose decides whether giving a dose at `at` would double up.
//
//   - slotGiven: the same scheduled slot is already marked given (fixed schedule).
//   - nearby: other given doses of the same medication (any order).
//   - minInterval: family-configured minimum gap; nil/0 means "slot rule only".
//
// A gap of exactly minInterval is allowed.
func CheckDoubleDose(minInterval *int, slotGiven *GivenRecord, nearby []GivenRecord, at time.Time) *Conflict {
	hasInterval := minInterval != nil && *minInterval > 0

	if slotGiven != nil {
		c := &Conflict{Reason: "slot", LastGivenAt: slotGiven.At, LastGivenByName: slotGiven.ByName}
		if hasInterval {
			c.MinIntervalMinutes = minInterval
			af := slotGiven.At.Add(time.Duration(*minInterval) * time.Minute)
			c.AllowedFrom = &af
		}
		return c
	}
	if !hasInterval || len(nearby) == 0 {
		return nil
	}

	gap := time.Duration(*minInterval) * time.Minute
	var closest *GivenRecord
	best := time.Duration(math.MaxInt64)
	for i := range nearby {
		d := at.Sub(nearby[i].At)
		if d < 0 {
			d = -d
		}
		if d < best {
			best, closest = d, &nearby[i]
		}
	}
	if best >= gap {
		return nil
	}
	// The next allowed moment is measured from the latest dose at or before `at`.
	latest := closest
	for i := range nearby {
		if !nearby[i].At.After(at) && nearby[i].At.After(latest.At) {
			latest = &nearby[i]
		}
	}
	af := latest.At.Add(gap)
	return &Conflict{Reason: "interval", LastGivenAt: closest.At, LastGivenByName: closest.ByName,
		MinIntervalMinutes: minInterval, AllowedFrom: &af}
}

// ---------- schedule & missed doses ----------

// MissedAfter is how long a scheduled dose may stay pending before it counts as missed.
const MissedAfter = 30 * time.Minute

// IsMissed reports whether a scheduled pending dose is overdue at `now`.
func IsMissed(status string, scheduledAt *time.Time, now time.Time) bool {
	return status == "pending" && scheduledAt != nil && now.Sub(*scheduledAt) > MissedAfter
}

// ParseClock parses "HH:MM" (also accepts "HH:MM:SS" and "HH.MM").
func ParseClock(s string) (h, m int, err error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ".", ":")
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("jam %q tidak valid, gunakan format HH:MM", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("jam %q tidak valid, gunakan format HH:MM", s)
	}
	return h, m, nil
}

// NormalizeClock returns "HH:MM".
func NormalizeClock(s string) (string, error) {
	h, m, err := ParseClock(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}

// SlotsForDate returns the scheduled instants of a medication on a local calendar day.
// startDate/endDate are "YYYY-MM-DD" (inclusive) or nil.
func SlotsForDate(times []string, startDate, endDate *string, day time.Time, loc *time.Location) []time.Time {
	y, mo, d := day.In(loc).Date()
	dayStr := fmt.Sprintf("%04d-%02d-%02d", y, mo, d)
	if startDate != nil && dayStr < *startDate {
		return nil
	}
	if endDate != nil && dayStr > *endDate {
		return nil
	}
	var out []time.Time
	for _, t := range times {
		h, m, err := ParseClock(t)
		if err != nil {
			continue
		}
		out = append(out, time.Date(y, mo, d, h, m, 0, 0, loc))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// DayBounds returns [start of local day, start of next local day).
func DayBounds(t time.Time, loc *time.Location) (time.Time, time.Time) {
	y, m, d := t.In(loc).Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}

// ---------- rule alerts ----------

// AlertDraft is a rule result before it is persisted.
type AlertDraft struct {
	Severity string
	Title    string
	Message  string
	Metric   string
}

type TempReading struct {
	At      time.Time
	Celsius float64
}

const (
	FeverThreshold     = 38.0
	HighFeverThreshold = 39.5
	SystolicThreshold  = 160
)

// FeverStreakStart returns the first day of a run of >= 3 consecutive local
// calendar days with a reading >= 38.0 that ends on the latest fever day.
func FeverStreakStart(readings []TempReading, loc *time.Location) (time.Time, bool) {
	days := map[string]time.Time{}
	for _, r := range readings {
		if r.Celsius >= FeverThreshold {
			s, _ := DayBounds(r.At, loc)
			days[s.Format("2006-01-02")] = s
		}
	}
	if len(days) < 3 {
		return time.Time{}, false
	}
	var latest time.Time
	for _, d := range days {
		if d.After(latest) {
			latest = d
		}
	}
	start, n := latest, 1
	for {
		prev := start.AddDate(0, 0, -1)
		if _, ok := days[prev.Format("2006-01-02")]; !ok {
			break
		}
		start, n = prev, n+1
	}
	return start, n >= 3
}

// EvaluateTemperature runs the fever rules for an `anak` (and any) profile.
func EvaluateTemperature(profileType, name string, latest TempReading, recent []TempReading, loc *time.Location) []AlertDraft {
	var out []AlertDraft
	if latest.Celsius >= HighFeverThreshold {
		out = append(out, AlertDraft{
			Severity: "penting", Metric: "suhu",
			Title: fmt.Sprintf("Suhu %s sangat tinggi", name),
			Message: fmt.Sprintf("Suhu %s tercatat %s° pukul %s. Pertimbangkan segera menghubungi tenaga kesehatan.",
				name, FormatDecimal(latest.Celsius), latest.At.In(loc).Format("15.04")),
		})
	}
	if profileType == "anak" {
		if start, ok := FeverStreakStart(append(recent, latest), loc); ok {
			_, end := DayBounds(latest.At, loc)
			days := int(end.Sub(start).Hours() / 24)
			out = append(out, AlertDraft{
				Severity: "penting", Metric: "suhu",
				Title: fmt.Sprintf("Suhu %s di atas 38° tiga hari berturut-turut", name),
				Message: fmt.Sprintf("Suhu %s di atas 38° sejak %s (%d hari). Pertimbangkan untuk menghubungi dokter atau puskesmas.",
					name, DayName(start), days),
			})
		}
	}
	return out
}

// EvaluateSystolic checks the last three blood pressure readings (newest first).
func EvaluateSystolic(profileType, name string, lastSystolic []int) []AlertDraft {
	if profileType != "lansia" && profileType != "kronis" {
		return nil
	}
	if len(lastSystolic) < 3 {
		return nil
	}
	for _, v := range lastSystolic[:3] {
		if v < SystolicThreshold {
			return nil
		}
	}
	return []AlertDraft{{
		Severity: "penting", Metric: "tensi",
		Title: fmt.Sprintf("Tensi %s tinggi tiga kali berturut-turut", name),
		Message: fmt.Sprintf("Tiga pengukuran terakhir tensi atas %s: %d, %d, dan %d. Pertimbangkan untuk berkonsultasi dengan dokter.",
			name, lastSystolic[2], lastSystolic[1], lastSystolic[0]),
	}}
}

// ---------- guardrail for AI text ----------

var forbiddenPhrases = []string{
	"diagnosis", "didiagnosis", "mendiagnosis", "anda menderita", "kamu menderita", "menderita penyakit",
	"dosis yang disarankan", "dosis yang dianjurkan", "naikkan dosis", "turunkan dosis", "tambah dosis",
	"hentikan obat", "berhenti minum obat", "ganti obat", "overdosis", "kemungkinan besar terkena",
}

// ContainsForbidden reports whether AI text crosses the medical guardrail.
func ContainsForbidden(text string) bool {
	t := strings.ToLower(text)
	for _, p := range forbiddenPhrases {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// ---------- formatting helpers (Bahasa Indonesia) ----------

// FormatDecimal renders 38.5 as "38,5".
func FormatDecimal(v float64) string {
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64)
	return strings.Replace(s, ".", ",", 1)
}

var dayNames = [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}

func DayName(t time.Time) string { return dayNames[t.Weekday()] }

var monthNames = [...]string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}

func DateID(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), monthNames[t.Month()], t.Year())
}
