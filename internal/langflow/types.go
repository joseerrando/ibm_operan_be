// Package langflow calls the five Operan flows and defines their contracts.
package langflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type FlowName string

const (
	ExtractVoice FlowName = "extract_voice_log"
	Handover     FlowName = "handover_summary"
	Trend        FlowName = "trend_analysis"
	DoctorReport FlowName = "doctor_report"
	AskHistory   FlowName = "ask_history"
)

// Runner executes a flow with a JSON payload and decodes the result into out.
// out may be *string to receive the raw text (used by ask_history).
type Runner interface {
	Run(ctx context.Context, flow FlowName, payload any, out any) error
	Mock() bool
}

// ---------- shared payload items ----------

type LogItem struct {
	ID         string          `json:"id,omitempty"`
	Type       string          `json:"type"`
	Value      json.RawMessage `json:"value"`
	RecordedAt string          `json:"recorded_at"`
	By         string          `json:"by,omitempty"`
}

type DoseItem struct {
	ID          string `json:"id,omitempty"`
	Medication  string `json:"medication"`
	Kind        string `json:"kind"`
	Status      string `json:"status"` // given | pending | skipped | missed
	ScheduledAt string `json:"scheduled_at,omitempty"`
	GivenAt     string `json:"given_at,omitempty"`
	By          string `json:"by,omitempty"`
}

type AlertItem struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

// ---------- Flow 1: extract_voice_log ----------

type VoiceMed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type VoiceInput struct {
	ProfileType string     `json:"profile_type"`
	ProfileName string     `json:"profile_name"`
	Now         string     `json:"now"`
	Medications []VoiceMed `json:"medications"`
	Transcript  string     `json:"transcript"`
}

type ExtractedLog struct {
	LogType    string          `json:"log_type"`
	Value      json.RawMessage `json:"value"`
	RecordedAt string          `json:"recorded_at"`
}

type ExtractedDose struct {
	MedicationID string `json:"medication_id"`
	GivenAt      string `json:"given_at"`
}

type VoiceOutput struct {
	Logs          []ExtractedLog  `json:"logs"`
	Doses         []ExtractedDose `json:"doses"`
	UnmatchedText string          `json:"unmatched_text"`
}

var validLogTypes = map[string]bool{"suhu": true, "tensi": true, "gula_darah": true, "makan": true,
	"tidur": true, "bab_bak": true, "keluhan": true, "catatan": true}

func (o *VoiceOutput) Validate() error {
	if o.Logs == nil {
		o.Logs = []ExtractedLog{}
	}
	if o.Doses == nil {
		o.Doses = []ExtractedDose{}
	}
	for i, l := range o.Logs {
		if !validLogTypes[l.LogType] {
			return fmt.Errorf("logs[%d].log_type %q tidak dikenal", i, l.LogType)
		}
		if len(l.Value) == 0 || !json.Valid(l.Value) {
			return fmt.Errorf("logs[%d].value harus objek JSON", i)
		}
	}
	for i, d := range o.Doses {
		if d.MedicationID == "" {
			return fmt.Errorf("doses[%d].medication_id kosong", i)
		}
	}
	return nil
}

// ---------- Flow 2: handover_summary ----------

type HandoverInput struct {
	ProfileType string      `json:"profile_type"`
	ProfileName string      `json:"profile_name"`
	Caregiver   string      `json:"caregiver"`
	ShiftStart  string      `json:"shift_start"`
	ShiftEnd    string      `json:"shift_end"`
	Logs        []LogItem   `json:"logs"`
	Doses       []DoseItem  `json:"doses"`
	Alerts      []AlertItem `json:"alerts"`
}

type HandoverOutput struct {
	Summary      string   `json:"summary"`
	PendingItems []string `json:"pending_items"`
	WatchItems   []string `json:"watch_items"`
}

func (o *HandoverOutput) Validate() error {
	if strings.TrimSpace(o.Summary) == "" {
		return fmt.Errorf("summary kosong")
	}
	if o.PendingItems == nil {
		o.PendingItems = []string{}
	}
	if o.WatchItems == nil {
		o.WatchItems = []string{}
	}
	return nil
}

// ---------- Flow 3: trend_analysis ----------

type TrendInput struct {
	ProfileType string     `json:"profile_type"`
	ProfileName string     `json:"profile_name"`
	Now         string     `json:"now"`
	Logs        []LogItem  `json:"logs"`
	Doses       []DoseItem `json:"doses"`
}

type TrendAlert struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

type TrendOutput struct {
	Alerts []TrendAlert `json:"alerts"`
}

func (o *TrendOutput) Validate() error {
	if o.Alerts == nil {
		o.Alerts = []TrendAlert{}
	}
	for i, a := range o.Alerts {
		switch a.Severity {
		case "info", "perhatian", "penting":
		default:
			return fmt.Errorf("alerts[%d].severity %q tidak valid", i, a.Severity)
		}
		if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Message) == "" {
			return fmt.Errorf("alerts[%d] butuh title dan message", i)
		}
	}
	return nil
}

// ---------- Flow 4: doctor_report ----------

type ReportInput struct {
	ProfileType string      `json:"profile_type"`
	ProfileName string      `json:"profile_name"`
	Age         string      `json:"age,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	PeriodStart string      `json:"period_start"`
	PeriodEnd   string      `json:"period_end"`
	Logs        []LogItem   `json:"logs"`
	Doses       []DoseItem  `json:"doses"`
	Alerts      []AlertItem `json:"alerts"`
}

type Adherence struct {
	Name   string `json:"name"`
	Given  int    `json:"given"`
	Missed int    `json:"missed"`
}

type ReportOutput struct {
	Overview            string      `json:"overview"`
	MedicationAdherence []Adherence `json:"medication_adherence"`
	KeyObservations     []string    `json:"key_observations"`
	Symptoms            []string    `json:"symptoms"`
	QuestionsForDoctor  []string    `json:"questions_for_doctor"`
}

func (o *ReportOutput) Validate() error {
	if strings.TrimSpace(o.Overview) == "" {
		return fmt.Errorf("overview kosong")
	}
	for _, p := range []*[]string{&o.KeyObservations, &o.Symptoms, &o.QuestionsForDoctor} {
		if *p == nil {
			*p = []string{}
		}
	}
	if o.MedicationAdherence == nil {
		o.MedicationAdherence = []Adherence{}
	}
	return nil
}

// ---------- Flow 5: ask_history ----------

type AskInput struct {
	CareProfileID string `json:"care_profile_id"`
	ProfileName   string `json:"profile_name"`
	Question      string `json:"question"`
	Now           string `json:"now"`
	// Tools: base URL + short-lived token scoped to this profile only.
	ToolBaseURL string `json:"tool_base_url"`
	ToolToken   string `json:"tool_token"`
}

type AskOutput struct {
	Answer  string   `json:"answer"`
	Sources []string `json:"sources"`
}

// ParseAsk accepts either JSON {answer, sources} or plain text.
func ParseAsk(text string) AskOutput {
	if js, err := ExtractJSON(text); err == nil {
		var o AskOutput
		if json.Unmarshal([]byte(js), &o) == nil && strings.TrimSpace(o.Answer) != "" {
			if o.Sources == nil {
				o.Sources = []string{}
			}
			return o
		}
	}
	return AskOutput{Answer: strings.TrimSpace(text), Sources: []string{}}
}

// validator is implemented by every structured output.
type validator interface{ Validate() error }
