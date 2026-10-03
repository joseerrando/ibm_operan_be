package service

import (
	"strings"
	"testing"
	"time"
)

var wib = time.FixedZone("WIB", 7*3600)

func at(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, wib) }

func intp(v int) *int { return &v }

func TestCheckDoubleDose(t *testing.T) {
	ibu07 := GivenRecord{At: at(2, 7, 0), ByName: "Ibu"}

	tests := []struct {
		name        string
		interval    *int
		slotGiven   *GivenRecord
		nearby      []GivenRecord
		at          time.Time
		wantReason  string // "" = allowed
		wantAllowed *time.Time
	}{
		{name: "PRN dalam jeda ditolak", interval: intp(240), nearby: []GivenRecord{ibu07}, at: at(2, 9, 30),
			wantReason: "interval", wantAllowed: ptr(at(2, 11, 0))},
		{name: "PRN di luar jeda boleh", interval: intp(240), nearby: []GivenRecord{ibu07}, at: at(2, 11, 1)},
		{name: "batas tepat di menit jeda boleh", interval: intp(240), nearby: []GivenRecord{ibu07}, at: at(2, 11, 0)},
		{name: "satu menit sebelum batas ditolak", interval: intp(240), nearby: []GivenRecord{ibu07}, at: at(2, 10, 59),
			wantReason: "interval", wantAllowed: ptr(at(2, 11, 0))},
		{name: "PRN pertama kali boleh", interval: intp(240), at: at(2, 9, 0)},
		{name: "jadwal tetap slot sama sudah diberikan ditolak", slotGiven: &ibu07, at: at(2, 7, 30), wantReason: "slot"},
		{name: "jadwal tetap slot berbeda boleh", nearby: []GivenRecord{ibu07}, at: at(2, 19, 0)},
		{name: "jadwal tetap dengan jeda, slot berbeda tapi terlalu dekat", interval: intp(360),
			nearby: []GivenRecord{ibu07}, at: at(2, 12, 0), wantReason: "interval", wantAllowed: ptr(at(2, 13, 0))},
		{name: "pencatatan mundur (suara) terlalu dekat dengan dosis sesudahnya", interval: intp(240),
			nearby: []GivenRecord{{At: at(2, 9, 0), ByName: "Ibu"}}, at: at(2, 7, 0), wantReason: "interval"},
		{name: "tanpa jeda dan tanpa slot tidak dicek", nearby: []GivenRecord{ibu07}, at: at(2, 7, 5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := CheckDoubleDose(tt.interval, tt.slotGiven, tt.nearby, tt.at)
			if tt.wantReason == "" {
				if c != nil {
					t.Fatalf("want allowed, got conflict %+v", c)
				}
				return
			}
			if c == nil {
				t.Fatalf("want conflict %q, got allowed", tt.wantReason)
			}
			if c.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", c.Reason, tt.wantReason)
			}
			if c.LastGivenByName == "" {
				t.Error("conflict must name who gave the last dose")
			}
			if tt.wantAllowed != nil && (c.AllowedFrom == nil || !c.AllowedFrom.Equal(*tt.wantAllowed)) {
				t.Errorf("allowed_from = %v, want %v", c.AllowedFrom, *tt.wantAllowed)
			}
		})
	}
}

// force=true is applied by the caller: the rule still reports the conflict so it can be stored.
func TestCheckDoubleDoseForceIsCallerDecision(t *testing.T) {
	c := CheckDoubleDose(intp(240), nil, []GivenRecord{{At: at(2, 7, 0), ByName: "Ibu"}}, at(2, 8, 0))
	if c == nil {
		t.Fatal("conflict must still be detected so a forced dose is flagged")
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestIsMissed(t *testing.T) {
	sched := at(2, 12, 0)
	cases := []struct {
		status string
		now    time.Time
		want   bool
	}{
		{"pending", at(2, 12, 30), false}, // tepat 30 menit belum terlewat
		{"pending", at(2, 12, 31), true},
		{"pending", at(2, 11, 0), false},
		{"given", at(2, 14, 0), false},
		{"skipped", at(2, 14, 0), false},
	}
	for _, c := range cases {
		if got := IsMissed(c.status, &sched, c.now); got != c.want {
			t.Errorf("IsMissed(%s, now=%s) = %v, want %v", c.status, c.now.Format("15:04"), got, c.want)
		}
	}
	if IsMissed("pending", nil, at(2, 23, 0)) {
		t.Error("PRN doses (no schedule) can never be missed")
	}
}

func TestSlotsForDate(t *testing.T) {
	start, end := "2026-10-02", "2026-10-03"
	slots := SlotsForDate([]string{"19:00", "07:00"}, &start, &end, at(2, 1, 0), wib)
	if len(slots) != 2 || !slots[0].Equal(at(2, 7, 0)) || !slots[1].Equal(at(2, 19, 0)) {
		t.Fatalf("slots = %v", slots)
	}
	if got := SlotsForDate([]string{"07:00"}, &start, &end, at(4, 1, 0), wib); len(got) != 0 {
		t.Errorf("after end date should be empty, got %v", got)
	}
	if got := SlotsForDate([]string{"07:00"}, &start, nil, at(1, 1, 0), wib); len(got) != 0 {
		t.Errorf("before start date should be empty, got %v", got)
	}
}

func TestFeverRuleThreeDifferentDays(t *testing.T) {
	recent := []TempReading{{at(1, 8, 0), 38.2}, {at(2, 20, 0), 38.6}}
	alerts := EvaluateTemperature("anak", "Adik", TempReading{at(3, 7, 0), 38.1}, recent, wib)
	if len(alerts) != 1 || alerts[0].Severity != "penting" {
		t.Fatalf("want 1 penting alert, got %+v", alerts)
	}
	if !strings.Contains(alerts[0].Message, "Kamis") { // 1 Okt 2026 = Kamis
		t.Errorf("message should name the start day: %s", alerts[0].Message)
	}
}

func TestFeverRuleSameDayDoesNotTrigger(t *testing.T) {
	recent := []TempReading{{at(2, 6, 0), 38.4}, {at(2, 12, 0), 38.9}}
	if alerts := EvaluateTemperature("anak", "Adik", TempReading{at(2, 18, 0), 38.5}, recent, wib); len(alerts) != 0 {
		t.Fatalf("3 readings on the same day must not trigger, got %+v", alerts)
	}
}

func TestFeverRuleGapBreaksStreak(t *testing.T) {
	recent := []TempReading{{at(1, 8, 0), 38.2}, {at(2, 8, 0), 37.2}, {at(3, 8, 0), 38.5}}
	if alerts := EvaluateTemperature("anak", "Adik", TempReading{at(4, 8, 0), 38.3}, recent, wib); len(alerts) != 0 {
		t.Fatalf("non-consecutive fever days must not trigger, got %+v", alerts)
	}
}

func TestFeverStreakOnlyForChildren(t *testing.T) {
	recent := []TempReading{{at(1, 8, 0), 38.2}, {at(2, 8, 0), 38.2}}
	if alerts := EvaluateTemperature("lansia", "Kakek", TempReading{at(3, 8, 0), 38.3}, recent, wib); len(alerts) != 0 {
		t.Fatalf("streak rule applies to anak only, got %+v", alerts)
	}
}

func TestHighFeverAnyProfile(t *testing.T) {
	alerts := EvaluateTemperature("pemulihan", "Ayah", TempReading{at(2, 22, 15), 39.6}, nil, wib)
	if len(alerts) != 1 || !strings.Contains(alerts[0].Message, "39,6") || !strings.Contains(alerts[0].Message, "22.15") {
		t.Fatalf("got %+v", alerts)
	}
}

func TestSystolicRule(t *testing.T) {
	if a := EvaluateSystolic("lansia", "Kakek", []int{165, 162, 160}); len(a) != 1 {
		t.Fatalf("3x >= 160 should alert, got %+v", a)
	}
	if a := EvaluateSystolic("lansia", "Kakek", []int{165, 150, 170}); len(a) != 0 {
		t.Fatalf("broken streak should not alert, got %+v", a)
	}
	if a := EvaluateSystolic("kronis", "Kakek", []int{165, 162}); len(a) != 0 {
		t.Fatalf("fewer than 3 readings should not alert, got %+v", a)
	}
	if a := EvaluateSystolic("anak", "Adik", []int{165, 162, 161}); len(a) != 0 {
		t.Fatalf("rule is for lansia/kronis only, got %+v", a)
	}
}

func TestRuleTextsAreSafe(t *testing.T) {
	var all []AlertDraft
	all = append(all, EvaluateTemperature("anak", "Adik", TempReading{at(3, 7, 0), 39.8},
		[]TempReading{{at(1, 8, 0), 38.2}, {at(2, 8, 0), 38.2}}, wib)...)
	all = append(all, EvaluateSystolic("lansia", "Kakek", []int{170, 165, 161})...)
	for _, a := range all {
		text := a.Title + " " + a.Message
		if ContainsForbidden(text) {
			t.Errorf("rule text crosses guardrail: %s", text)
		}
		if !strings.Contains(strings.ToLower(a.Message), "pertimbangkan") {
			t.Errorf("rule text must end with a consultation suggestion: %s", a.Message)
		}
	}
}

func TestContainsForbidden(t *testing.T) {
	if !ContainsForbidden("Berdasarkan data, Anda menderita hipertensi") {
		t.Error("should flag diagnosis wording")
	}
	if !ContainsForbidden("Dosis yang disarankan adalah 5 ml") {
		t.Error("should flag dosing advice")
	}
	if ContainsForbidden("Suhu Adik di atas 38° sejak Selasa. Pertimbangkan untuk menghubungi dokter.") {
		t.Error("normal alert text must pass")
	}
}

func TestFormatDecimal(t *testing.T) {
	if got := FormatDecimal(38.45); got != "38,5" {
		t.Errorf("got %s", got)
	}
	if got := FormatDecimal(37); got != "37,0" {
		t.Errorf("got %s", got)
	}
}
