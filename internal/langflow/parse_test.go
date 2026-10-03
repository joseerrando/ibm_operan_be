package langflow

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExtractTextDocumentedPath(t *testing.T) {
	body := `{"outputs":[{"outputs":[{"results":{"message":{"text":"{\"summary\":\"ok\"}"}}}]}]}`
	got, err := ExtractText([]byte(body))
	if err != nil || got != `{"summary":"ok"}` {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractTextFallbacks(t *testing.T) {
	cases := map[string]string{
		"messages":  `{"outputs":[{"outputs":[{"messages":[{"message":"halo"}]}]}]}`,
		"artifacts": `{"outputs":[{"outputs":[{"artifacts":{"message":"halo"}}]}]}`,
		"nested":    `{"outputs":[{"outputs":[{"results":{"text":{"data":{"text":"halo"}}}}]}]}`,
	}
	for name, body := range cases {
		got, err := ExtractText([]byte(body))
		if err != nil || got != "halo" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	if _, err := ExtractText([]byte(`{"outputs":[]}`)); err == nil {
		t.Error("empty outputs should error")
	}
	if _, err := ExtractText([]byte(`not json`)); err == nil {
		t.Error("invalid JSON body should error")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		"fence":        "```json\n{\"a\":1}\n```",
		"fence upper":  "```JSON\n{\"a\":1}\n```",
		"bare fence":   "```\n{\"a\":1}\n```",
		"prose before": "Berikut hasilnya:\n{\"a\":1}",
		"prose after":  "{\"a\":1}\nSemoga membantu.",
		"plain":        `{"a":1}`,
	}
	for name, in := range cases {
		got, err := ExtractJSON(in)
		if err != nil || got != `{"a":1}` {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	for _, bad := range []string{"tidak ada json", "{\"a\": }", "```json\n{\"a\":\n```"} {
		if _, err := ExtractJSON(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestDecodeOutputValidatesSchema(t *testing.T) {
	var ho HandoverOutput
	if err := DecodeOutput(`{"summary":""}`, &ho); err == nil {
		t.Error("empty summary must fail schema validation")
	}
	var tr TrendOutput
	if err := DecodeOutput(`{"alerts":[{"severity":"bahaya","title":"x","message":"y"}]}`, &tr); err == nil {
		t.Error("unknown severity must fail")
	}
	var vo VoiceOutput
	if err := DecodeOutput(`{"logs":[{"log_type":"nadi","value":{}}]}`, &vo); err == nil {
		t.Error("unknown log_type must fail")
	}
}

func TestTestdataMatchesSchemas(t *testing.T) {
	targets := map[string]any{
		"extract_voice_log.json": &VoiceOutput{},
		"handover_summary.json":  &HandoverOutput{},
		"trend_analysis.json":    &TrendOutput{},
		"doctor_report.json":     &ReportOutput{},
	}
	for file, out := range targets {
		b, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if err := DecodeOutput(string(b), out); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
}

func TestParseAsk(t *testing.T) {
	if o := ParseAsk(`{"answer":"Jam 07.00","sources":["x"]}`); o.Answer != "Jam 07.00" || len(o.Sources) != 1 {
		t.Errorf("json form: %+v", o)
	}
	if o := ParseAsk("Jam 07.00 tadi pagi."); o.Answer != "Jam 07.00 tadi pagi." {
		t.Errorf("plain form: %+v", o)
	}
}

var wib = time.FixedZone("WIB", 7*3600)

func TestMockExtractVoiceExample(t *testing.T) {
	m := &MockRunner{Loc: wib}
	in := VoiceInput{ProfileType: "anak", ProfileName: "Adik", Now: "2026-10-02T09:15:00+07:00",
		Medications: []VoiceMed{{ID: "med-pct", Name: "Paracetamol sirup", Kind: "obat"}, {ID: "med-vit", Name: "Vitamin D", Kind: "vitamin"}},
		Transcript:  "Adik suhunya 38,5, sudah minum obat penurun panas jam 9"}
	var out VoiceOutput
	if err := m.Run(context.Background(), ExtractVoice, in, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Logs) != 1 || out.Logs[0].LogType != "suhu" || !strings.Contains(string(out.Logs[0].Value), "38.5") {
		t.Fatalf("logs = %+v", out.Logs)
	}
	if len(out.Doses) != 1 || out.Doses[0].MedicationID != "med-pct" || out.Doses[0].GivenAt != "2026-10-02T09:00:00+07:00" {
		t.Fatalf("doses = %+v", out.Doses)
	}
}

func TestMockOutputsPassGuardrailWords(t *testing.T) {
	m := &MockRunner{Loc: wib}
	logs := []LogItem{}
	for d := 1; d <= 4; d++ {
		ts := time.Date(2026, 10, d, 8, 0, 0, 0, wib).Format(time.RFC3339)
		v, _ := json.Marshal(map[string]any{"celsius": 38.4})
		logs = append(logs, LogItem{Type: "suhu", Value: v, RecordedAt: ts})
		k, _ := json.Marshal(map[string]any{"text": "batuk"})
		logs = append(logs, LogItem{Type: "keluhan", Value: k, RecordedAt: ts})
	}
	var tr TrendOutput
	if err := m.Run(context.Background(), Trend, TrendInput{ProfileType: "anak", ProfileName: "Adik", Logs: logs}, &tr); err != nil {
		t.Fatal(err)
	}
	var rep ReportOutput
	if err := m.Run(context.Background(), DoctorReport, ReportInput{ProfileType: "anak", ProfileName: "Adik", Logs: logs}, &rep); err != nil {
		t.Fatal(err)
	}
	texts := []string{rep.Overview}
	texts = append(texts, rep.QuestionsForDoctor...)
	for _, a := range tr.Alerts {
		texts = append(texts, a.Title, a.Message)
	}
	for _, s := range texts {
		low := strings.ToLower(s)
		for _, bad := range []string{"diagnosis", "anda menderita", "dosis yang disarankan"} {
			if strings.Contains(low, bad) {
				t.Errorf("mock text contains forbidden phrase %q: %s", bad, s)
			}
		}
	}
}
