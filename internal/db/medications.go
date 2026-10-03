package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ---------- medications ----------

const medCols = `id, care_profile_id, name, kind, dose_label, schedule_times, is_prn, min_interval_minutes, start_date, end_date, is_active, created_at`

func scanMed(sc interface{ Scan(...any) error }) (*Medication, error) {
	var m Medication
	var times []byte
	var interval sql.NullInt64
	var start, end sql.NullTime
	if err := sc.Scan(&m.ID, &m.CareProfileID, &m.Name, &m.Kind, &m.DoseLabel, &times, &m.IsPRN, &interval, &start, &end, &m.IsActive, &m.CreatedAt); err != nil {
		return nil, err
	}
	m.ScheduleTimes = []string{}
	if len(times) > 0 {
		_ = json.Unmarshal(times, &m.ScheduleTimes)
	}
	if m.ScheduleTimes == nil {
		m.ScheduleTimes = []string{}
	}
	if interval.Valid {
		v := int(interval.Int64)
		m.MinIntervalMinutes = &v
	}
	m.StartDate, m.EndDate = datePtr(start), datePtr(end)
	return &m, nil
}

func medArgs(m *Medication) []any {
	var times any
	if !m.IsPRN && len(m.ScheduleTimes) > 0 {
		b, _ := json.Marshal(m.ScheduleTimes)
		times = b
	}
	var interval any
	if m.MinIntervalMinutes != nil {
		interval = *m.MinIntervalMinutes
	}
	return []any{m.Name, m.Kind, m.DoseLabel, times, m.IsPRN, interval, nullStr(m.StartDate), nullStr(m.EndDate), m.IsActive}
}

func (s *Store) CreateMedication(ctx context.Context, m *Medication) error {
	m.ID = NewID()
	m.CreatedAt = time.Now().UTC()
	m.IsActive = true
	args := append([]any{m.ID, m.CareProfileID}, medArgs(m)...)
	args = append(args, m.CreatedAt)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO medications (`+medCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return err
}

func (s *Store) UpdateMedication(ctx context.Context, m *Medication) error {
	args := append(medArgs(m), m.ID)
	_, err := s.DB.ExecContext(ctx, `UPDATE medications SET name=?, kind=?, dose_label=?, schedule_times=?, is_prn=?,
		min_interval_minutes=?, start_date=?, end_date=?, is_active=? WHERE id=?`, args...)
	return err
}

func (s *Store) MedicationByID(ctx context.Context, id string) (*Medication, error) {
	m, err := scanMed(s.DB.QueryRowContext(ctx, `SELECT `+medCols+` FROM medications WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return m, nil
}

func (s *Store) MedicationsByProfile(ctx context.Context, profileID string, activeOnly bool) ([]Medication, error) {
	q := `SELECT ` + medCols + ` FROM medications WHERE care_profile_id = ?`
	if activeOnly {
		q += ` AND is_active = 1`
	}
	return s.queryMeds(ctx, q+` ORDER BY is_prn, created_at`, profileID)
}

// ActiveScheduledMedications is used by the daily dose generator.
func (s *Store) ActiveScheduledMedications(ctx context.Context) ([]Medication, error) {
	return s.queryMeds(ctx, `SELECT `+medCols+` FROM medications WHERE is_active = 1 AND is_prn = 0`)
}

func (s *Store) queryMeds(ctx context.Context, q string, args ...any) ([]Medication, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Medication{}
	for rows.Next() {
		m, err := scanMed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ---------- doses ----------

const doseSelect = `SELECT d.id, d.medication_id, m.name, m.kind, d.scheduled_at, d.given_at, d.given_by, u.name,
	d.status, d.note, d.forced, d.missed_alerted, d.marked_at, d.created_at, m.care_profile_id
	FROM medication_doses d
	JOIN medications m ON m.id = d.medication_id
	LEFT JOIN users u ON u.id = d.given_by `

func scanDose(sc interface{ Scan(...any) error }) (*Dose, error) {
	var d Dose
	var sched, given, marked sql.NullTime
	var by, byName, note sql.NullString
	if err := sc.Scan(&d.ID, &d.MedicationID, &d.MedicationName, &d.Kind, &sched, &given, &by, &byName,
		&d.Status, &note, &d.Forced, &d.MissedAlerted, &marked, &d.CreatedAt, &d.CareProfileID); err != nil {
		return nil, err
	}
	d.ScheduledAt, d.GivenAt, d.MarkedAt = timePtr(sched), timePtr(given), timePtr(marked)
	d.GivenBy, d.GivenByName, d.Note = strPtr(by), strPtr(byName), strPtr(note)
	return &d, nil
}

func (s *Store) queryDoses(ctx context.Context, q string, args ...any) ([]Dose, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Dose{}
	for rows.Next() {
		d, err := scanDose(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) DoseByID(ctx context.Context, id string) (*Dose, error) {
	d, err := scanDose(s.DB.QueryRowContext(ctx, doseSelect+`WHERE d.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return d, nil
}

// InsertPendingDose creates a scheduled slot; existing slots are left untouched.
func (s *Store) InsertPendingDose(ctx context.Context, medID string, scheduledAt time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO medication_doses (id, medication_id, scheduled_at, status) VALUES (?,?,?, 'pending')`,
		NewID(), medID, scheduledAt.UTC())
	return err
}

// DeletePendingAfter removes future, untouched slots (used when a schedule changes).
func (s *Store) DeletePendingAfter(ctx context.Context, medID string, after time.Time) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM medication_doses WHERE medication_id = ? AND status = 'pending' AND scheduled_at > ?`,
		medID, after.UTC())
	return err
}

// DosesForProfile returns slots scheduled in [from,to) plus unscheduled (PRN/extra) doses given in that range.
func (s *Store) DosesForProfile(ctx context.Context, profileID string, from, to time.Time) ([]Dose, error) {
	return s.queryDoses(ctx, doseSelect+`WHERE m.care_profile_id = ? AND
		((d.scheduled_at >= ? AND d.scheduled_at < ?) OR (d.scheduled_at IS NULL AND d.given_at >= ? AND d.given_at < ?))
		ORDER BY COALESCE(d.scheduled_at, d.given_at)`, profileID, from.UTC(), to.UTC(), from.UTC(), to.UTC())
}

// GivenDosesNear returns given doses of a medication within ±window of t.
func (s *Store) GivenDosesNear(ctx context.Context, medID string, t time.Time, window time.Duration) ([]Dose, error) {
	return s.queryDoses(ctx, doseSelect+`WHERE d.medication_id = ? AND d.status = 'given' AND d.given_at BETWEEN ? AND ?
		ORDER BY d.given_at DESC`, medID, t.Add(-window).UTC(), t.Add(window).UTC())
}

// LastGivenDose returns the most recent given dose of a medication, or nil.
func (s *Store) LastGivenDose(ctx context.Context, medID string) (*Dose, error) {
	d, err := scanDose(s.DB.QueryRowContext(ctx, doseSelect+`WHERE d.medication_id = ? AND d.status = 'given' ORDER BY d.given_at DESC LIMIT 1`, medID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

func (s *Store) MarkDoseGiven(ctx context.Context, doseID string, givenAt time.Time, by string, note *string, forced bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE medication_doses SET status='given', given_at=?, given_by=?, note=?, forced=?, marked_at=UTC_TIMESTAMP(3) WHERE id=?`,
		givenAt.UTC(), by, nullStr(note), forced, doseID)
	return err
}

// InsertGivenDose records an unscheduled administration (PRN, or an extra dose from voice).
func (s *Store) InsertGivenDose(ctx context.Context, medID string, givenAt time.Time, by string, note *string, forced bool) (string, error) {
	id := NewID()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO medication_doses (id, medication_id, scheduled_at, given_at, given_by, status, note, forced, marked_at)
		VALUES (?,?,NULL,?,?,'given',?,?,UTC_TIMESTAMP(3))`, id, medID, givenAt.UTC(), by, nullStr(note), forced)
	return id, err
}

func (s *Store) SkipDose(ctx context.Context, doseID, by string, note *string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE medication_doses SET status='skipped', given_by=?, given_at=NULL, note=?, marked_at=UTC_TIMESTAMP(3) WHERE id=?`,
		by, nullStr(note), doseID)
	return err
}

// UndoDose reverts a scheduled slot to pending, or deletes an unscheduled dose.
func (s *Store) UndoDose(ctx context.Context, d *Dose) error {
	if d.ScheduledAt == nil {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM medication_doses WHERE id = ?`, d.ID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE medication_doses SET status='pending', given_at=NULL, given_by=NULL, note=NULL, forced=0, marked_at=NULL WHERE id=?`, d.ID)
	return err
}

// PendingDosesBefore lists overdue slots that have not been alerted yet.
func (s *Store) PendingDosesBefore(ctx context.Context, cutoff time.Time) ([]Dose, error) {
	return s.queryDoses(ctx, doseSelect+`WHERE d.status='pending' AND d.missed_alerted = 0 AND d.scheduled_at < ? AND m.is_active = 1`, cutoff.UTC())
}

func (s *Store) MarkMissedAlerted(ctx context.Context, doseID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE medication_doses SET missed_alerted = 1 WHERE id = ?`, doseID)
	return err
}
