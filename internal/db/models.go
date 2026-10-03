package db

import (
	"encoding/json"
	"time"
)

type User struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Family struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	InviteCode string    `json:"invite_code"`
	CreatedBy  *string   `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	Role       string    `json:"role,omitempty"` // role user saat ini di keluarga ini
}

type Member struct {
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
	// Diisi service: giliran jaga aktif dan terakhir.
	OnDutyFor   []string   `json:"on_duty_for"`
	LastShiftAt *time.Time `json:"last_shift_at"`
}

type Profile struct {
	ID             string    `json:"id"`
	FamilyID       string    `json:"family_id"`
	Name           string    `json:"name"`
	Nickname       *string   `json:"nickname"`
	ProfileType    string    `json:"profile_type"`
	BirthDate      *string   `json:"birth_date"`
	Notes          *string   `json:"notes"`
	TrackedMetrics []string  `json:"tracked_metrics"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
}

// DisplayName is the nickname when set, otherwise the full name.
func (p Profile) DisplayName() string {
	if p.Nickname != nil && *p.Nickname != "" {
		return *p.Nickname
	}
	return p.Name
}

type Medication struct {
	ID                 string    `json:"id"`
	CareProfileID      string    `json:"care_profile_id"`
	Name               string    `json:"name"`
	Kind               string    `json:"kind"`
	DoseLabel          string    `json:"dose_label"`
	ScheduleTimes      []string  `json:"schedule_times"`
	IsPRN              bool      `json:"is_prn"`
	MinIntervalMinutes *int      `json:"min_interval_minutes"`
	StartDate          *string   `json:"start_date"`
	EndDate            *string   `json:"end_date"`
	IsActive           bool      `json:"is_active"`
	CreatedAt          time.Time `json:"created_at"`
}

type Dose struct {
	ID             string     `json:"id"`
	MedicationID   string     `json:"medication_id"`
	MedicationName string     `json:"medication_name"`
	Kind           string     `json:"kind"`
	ScheduledAt    *time.Time `json:"scheduled_at"`
	GivenAt        *time.Time `json:"given_at"`
	GivenBy        *string    `json:"given_by"`
	GivenByName    *string    `json:"given_by_name"`
	Status         string     `json:"status"`
	Note           *string    `json:"note"`
	Forced         bool       `json:"forced"`
	MissedAlerted  bool       `json:"-"`
	MarkedAt       *time.Time `json:"marked_at"`
	CreatedAt      time.Time  `json:"created_at"`
	CareProfileID  string     `json:"care_profile_id,omitempty"`
}

type CareLog struct {
	ID            string          `json:"id"`
	CareProfileID string          `json:"care_profile_id"`
	AuthorID      *string         `json:"author_id"`
	AuthorName    *string         `json:"author_name"`
	LogType       string          `json:"log_type"`
	Value         json.RawMessage `json:"value"`
	Source        string          `json:"source"`
	RecordedAt    time.Time       `json:"recorded_at"`
	CreatedAt     time.Time       `json:"created_at"`
}

type Shift struct {
	ID            string     `json:"id"`
	CareProfileID string     `json:"care_profile_id"`
	CaregiverID   *string    `json:"caregiver_id"`
	CaregiverName *string    `json:"caregiver_name"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
}

type Handover struct {
	ID            string     `json:"id"`
	CareProfileID string     `json:"care_profile_id"`
	FromShiftID   *string    `json:"from_shift_id"`
	FromUserID    *string    `json:"from_user_id"`
	FromUserName  *string    `json:"from_user_name"`
	ToUserID      *string    `json:"to_user_id"`
	ToUserName    *string    `json:"to_user_name"`
	Summary       string     `json:"summary"`
	PendingItems  []string   `json:"pending_items"`
	WatchItems    []string   `json:"watch_items"`
	AIGenerated   bool       `json:"ai_generated"`
	ReadAt        *time.Time `json:"read_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Alert struct {
	ID            string    `json:"id"`
	CareProfileID string    `json:"care_profile_id"`
	Source        string    `json:"source"`
	Severity      string    `json:"severity"`
	Title         string    `json:"title"`
	Message       string    `json:"message"`
	Metric        *string   `json:"metric"`
	IsRead        bool      `json:"is_read"`
	CreatedAt     time.Time `json:"created_at"`
}

type Report struct {
	ID            string          `json:"id"`
	CareProfileID string          `json:"care_profile_id"`
	PeriodStart   time.Time       `json:"period_start"`
	PeriodEnd     time.Time       `json:"period_end"`
	Content       json.RawMessage `json:"content"`
	CreatedBy     *string         `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
}
