package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ---------- care logs ----------

const logSelect = `SELECT l.id, l.care_profile_id, l.author_id, u.name, l.log_type, l.value, l.source, l.recorded_at, l.created_at
	FROM care_logs l LEFT JOIN users u ON u.id = l.author_id `

func scanLog(sc interface{ Scan(...any) error }) (*CareLog, error) {
	var l CareLog
	var author, authorName sql.NullString
	var value []byte
	if err := sc.Scan(&l.ID, &l.CareProfileID, &author, &authorName, &l.LogType, &value, &l.Source, &l.RecordedAt, &l.CreatedAt); err != nil {
		return nil, err
	}
	l.AuthorID, l.AuthorName = strPtr(author), strPtr(authorName)
	l.Value = json.RawMessage(value)
	return &l, nil
}

func (s *Store) queryLogs(ctx context.Context, q string, args ...any) ([]CareLog, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CareLog{}
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *Store) CreateLog(ctx context.Context, l *CareLog) error {
	l.ID = NewID()
	l.CreatedAt = time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO care_logs (id, care_profile_id, author_id, log_type, value, source, recorded_at, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, l.ID, l.CareProfileID, nullStr(l.AuthorID), l.LogType, []byte(l.Value), l.Source, l.RecordedAt.UTC(), l.CreatedAt)
	return err
}

func (s *Store) LogByID(ctx context.Context, id string) (*CareLog, error) {
	l, err := scanLog(s.DB.QueryRowContext(ctx, logSelect+`WHERE l.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return l, nil
}

// Logs returns logs in [from,to) newest first; logType "" means all types.
func (s *Store) Logs(ctx context.Context, profileID string, from, to time.Time, logType string, limit int) ([]CareLog, error) {
	q := logSelect + `WHERE l.care_profile_id = ? AND l.recorded_at >= ? AND l.recorded_at < ?`
	args := []any{profileID, from.UTC(), to.UTC()}
	if logType != "" {
		q += ` AND l.log_type = ?`
		args = append(args, logType)
	}
	q += ` ORDER BY l.recorded_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	return s.queryLogs(ctx, q, args...)
}

// LatestLogPerType returns the newest log of each type for a profile.
func (s *Store) LatestLogPerType(ctx context.Context, profileID string) ([]CareLog, error) {
	return s.queryLogs(ctx, logSelect+`JOIN (
			SELECT log_type, MAX(recorded_at) AS mx FROM care_logs WHERE care_profile_id = ? GROUP BY log_type
		) t ON t.log_type = l.log_type AND t.mx = l.recorded_at
		WHERE l.care_profile_id = ? ORDER BY l.recorded_at DESC`, profileID, profileID)
}

// RecentLogsOfType returns the last n logs of a type, newest first.
func (s *Store) RecentLogsOfType(ctx context.Context, profileID, logType string, n int) ([]CareLog, error) {
	return s.queryLogs(ctx, logSelect+`WHERE l.care_profile_id = ? AND l.log_type = ? ORDER BY l.recorded_at DESC LIMIT ?`,
		profileID, logType, n)
}

// ---------- shifts & handovers ----------

const shiftSelect = `SELECT s.id, s.care_profile_id, s.caregiver_id, u.name, s.started_at, s.ended_at
	FROM shifts s LEFT JOIN users u ON u.id = s.caregiver_id `

func scanShift(sc interface{ Scan(...any) error }) (*Shift, error) {
	var sh Shift
	var cg, cgName sql.NullString
	var ended sql.NullTime
	if err := sc.Scan(&sh.ID, &sh.CareProfileID, &cg, &cgName, &sh.StartedAt, &ended); err != nil {
		return nil, err
	}
	sh.CaregiverID, sh.CaregiverName, sh.EndedAt = strPtr(cg), strPtr(cgName), timePtr(ended)
	return &sh, nil
}

func (s *Store) ActiveShift(ctx context.Context, profileID string) (*Shift, error) {
	sh, err := scanShift(s.DB.QueryRowContext(ctx, shiftSelect+`WHERE s.care_profile_id = ? AND s.ended_at IS NULL ORDER BY s.started_at DESC LIMIT 1`, profileID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return sh, err
}

func (s *Store) ShiftByID(ctx context.Context, id string) (*Shift, error) {
	sh, err := scanShift(s.DB.QueryRowContext(ctx, shiftSelect+`WHERE s.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return sh, nil
}

// StartShift ends any open shift for the profile and opens a new one.
func (s *Store) StartShift(ctx context.Context, profileID, userID string, at time.Time) (*Shift, error) {
	if _, err := s.DB.ExecContext(ctx, `UPDATE shifts SET ended_at = ? WHERE care_profile_id = ? AND ended_at IS NULL`, at.UTC(), profileID); err != nil {
		return nil, err
	}
	id := NewID()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO shifts (id, care_profile_id, caregiver_id, started_at) VALUES (?,?,?,?)`,
		id, profileID, userID, at.UTC()); err != nil {
		return nil, err
	}
	return s.ShiftByID(ctx, id)
}

func (s *Store) EndShift(ctx context.Context, shiftID string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE shifts SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, at.UTC(), shiftID)
	return err
}

const handoverSelect = `SELECT h.id, h.care_profile_id, h.from_shift_id, h.from_user_id, fu.name, h.to_user_id, tu.name,
	h.summary, h.pending_items, h.watch_items, h.ai_generated, h.read_at, h.created_at
	FROM handovers h LEFT JOIN users fu ON fu.id = h.from_user_id LEFT JOIN users tu ON tu.id = h.to_user_id `

func scanHandover(sc interface{ Scan(...any) error }) (*Handover, error) {
	var h Handover
	var shift, from, fromName, to, toName sql.NullString
	var pending, watch []byte
	var read sql.NullTime
	if err := sc.Scan(&h.ID, &h.CareProfileID, &shift, &from, &fromName, &to, &toName, &h.Summary, &pending, &watch,
		&h.AIGenerated, &read, &h.CreatedAt); err != nil {
		return nil, err
	}
	h.FromShiftID, h.FromUserID, h.FromUserName = strPtr(shift), strPtr(from), strPtr(fromName)
	h.ToUserID, h.ToUserName, h.ReadAt = strPtr(to), strPtr(toName), timePtr(read)
	h.PendingItems, h.WatchItems = []string{}, []string{}
	_ = json.Unmarshal(pending, &h.PendingItems)
	_ = json.Unmarshal(watch, &h.WatchItems)
	if h.PendingItems == nil {
		h.PendingItems = []string{}
	}
	if h.WatchItems == nil {
		h.WatchItems = []string{}
	}
	return &h, nil
}

func (s *Store) CreateHandover(ctx context.Context, h *Handover) error {
	h.ID = NewID()
	h.CreatedAt = time.Now().UTC()
	pending, _ := json.Marshal(h.PendingItems)
	watch, _ := json.Marshal(h.WatchItems)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO handovers (id, care_profile_id, from_shift_id, from_user_id, to_user_id, summary,
		pending_items, watch_items, ai_generated, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.CareProfileID, nullStr(h.FromShiftID), nullStr(h.FromUserID), nullStr(h.ToUserID), h.Summary, pending, watch, h.AIGenerated, h.CreatedAt)
	return err
}

func (s *Store) HandoverByID(ctx context.Context, id string) (*Handover, error) {
	h, err := scanHandover(s.DB.QueryRowContext(ctx, handoverSelect+`WHERE h.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return h, nil
}

func (s *Store) LatestHandover(ctx context.Context, profileID string) (*Handover, error) {
	h, err := scanHandover(s.DB.QueryRowContext(ctx, handoverSelect+`WHERE h.care_profile_id = ? ORDER BY h.created_at DESC LIMIT 1`, profileID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return h, err
}

func (s *Store) MarkHandoverRead(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE handovers SET read_at = UTC_TIMESTAMP(3) WHERE id = ? AND read_at IS NULL`, id)
	return err
}

// ---------- alerts ----------

const alertCols = `id, care_profile_id, source, severity, title, message, metric, is_read, created_at`

func scanAlert(sc interface{ Scan(...any) error }) (*Alert, error) {
	var a Alert
	var metric sql.NullString
	if err := sc.Scan(&a.ID, &a.CareProfileID, &a.Source, &a.Severity, &a.Title, &a.Message, &metric, &a.IsRead, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.Metric = strPtr(metric)
	return &a, nil
}

func (s *Store) CreateAlert(ctx context.Context, a *Alert) error {
	a.ID = NewID()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO alerts (`+alertCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		a.ID, a.CareProfileID, a.Source, a.Severity, a.Title, a.Message, nullStr(a.Metric), a.IsRead, a.CreatedAt.UTC())
	return err
}

// Alerts lists alerts newest first; unreadOnly limits to active ones.
func (s *Store) Alerts(ctx context.Context, profileID string, unreadOnly bool, limit int) ([]Alert, error) {
	q := `SELECT ` + alertCols + ` FROM alerts WHERE care_profile_id = ?`
	if unreadOnly {
		q += ` AND is_read = 0`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, q, profileID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) AlertByID(ctx context.Context, id string) (*Alert, error) {
	a, err := scanAlert(s.DB.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alerts WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return a, nil
}

// HasRecentAlert is used to avoid duplicate alerts with the same title.
func (s *Store) HasRecentAlert(ctx context.Context, profileID, title string, since time.Time) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE care_profile_id = ? AND title = ? AND created_at >= ?`,
		profileID, title, since.UTC()).Scan(&n)
	return n > 0, err
}

func (s *Store) MarkAlertRead(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE alerts SET is_read = 1 WHERE id = ?`, id)
	return err
}

// ---------- reports ----------

func (s *Store) CreateReport(ctx context.Context, r *Report) error {
	r.ID = NewID()
	r.CreatedAt = time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO doctor_reports (id, care_profile_id, period_start, period_end, content, created_by, created_at)
		VALUES (?,?,?,?,?,?,?)`, r.ID, r.CareProfileID, r.PeriodStart.UTC(), r.PeriodEnd.UTC(), []byte(r.Content), nullStr(r.CreatedBy), r.CreatedAt)
	return err
}

func (s *Store) ReportByID(ctx context.Context, id string) (*Report, error) {
	var r Report
	var by sql.NullString
	var content []byte
	err := s.DB.QueryRowContext(ctx, `SELECT id, care_profile_id, period_start, period_end, content, created_by, created_at
		FROM doctor_reports WHERE id = ?`, id).Scan(&r.ID, &r.CareProfileID, &r.PeriodStart, &r.PeriodEnd, &content, &by, &r.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	r.Content, r.CreatedBy = json.RawMessage(content), strPtr(by)
	return &r, nil
}

func (s *Store) LatestReport(ctx context.Context, profileID string) (*Report, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM doctor_reports WHERE care_profile_id = ? ORDER BY created_at DESC LIMIT 1`, profileID).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.ReportByID(ctx, id)
}
