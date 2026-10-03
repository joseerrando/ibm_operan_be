package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// ---------- users ----------

func (s *Store) CreateUser(ctx context.Context, name, email, hash string) (*User, error) {
	u := &User{ID: NewID(), Name: name, Email: strings.ToLower(email), PasswordHash: hash, CreatedAt: time.Now().UTC()}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO users (id, name, email, password_hash, created_at) VALUES (?,?,?,?,?)`,
		u.ID, u.Name, u.Email, u.PasswordHash, u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) userBy(ctx context.Context, where string, arg any) (*User, error) {
	var u User
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, email, password_hash, created_at FROM users WHERE `+where, arg).
		Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return s.userBy(ctx, "email = ?", strings.ToLower(email))
}

func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return s.userBy(ctx, "id = ?", id)
}

func (s *Store) UpsertDeviceToken(ctx context.Context, userID, token, platform string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO device_tokens (id, user_id, fcm_token, platform, updated_at)
		VALUES (?,?,?,?,UTC_TIMESTAMP(3))
		ON DUPLICATE KEY UPDATE user_id = VALUES(user_id), platform = VALUES(platform), updated_at = UTC_TIMESTAMP(3)`,
		NewID(), userID, token, platform)
	return err
}

// ---------- families ----------

func (s *Store) CreateFamily(ctx context.Context, name, inviteCode, userID string) (*Family, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	f := &Family{ID: NewID(), Name: name, InviteCode: inviteCode, CreatedBy: &userID, CreatedAt: time.Now().UTC(), Role: "admin"}
	if _, err := tx.ExecContext(ctx, `INSERT INTO families (id, name, invite_code, created_by, created_at) VALUES (?,?,?,?,?)`,
		f.ID, f.Name, f.InviteCode, userID, f.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO family_members (id, family_id, user_id, role) VALUES (?,?,?, 'admin')`,
		NewID(), f.ID, userID); err != nil {
		return nil, err
	}
	return f, tx.Commit()
}

func (s *Store) FamilyByInviteCode(ctx context.Context, code string) (*Family, error) {
	var f Family
	var createdBy sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, invite_code, created_by, created_at FROM families WHERE invite_code = ?`, code).
		Scan(&f.ID, &f.Name, &f.InviteCode, &createdBy, &f.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	f.CreatedBy = strPtr(createdBy)
	return &f, nil
}

func (s *Store) FamilyByID(ctx context.Context, id string) (*Family, error) {
	var f Family
	var createdBy sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, invite_code, created_by, created_at FROM families WHERE id = ?`, id).
		Scan(&f.ID, &f.Name, &f.InviteCode, &createdBy, &f.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	f.CreatedBy = strPtr(createdBy)
	return &f, nil
}

// AddMember is idempotent: joining twice keeps the original role.
func (s *Store) AddMember(ctx context.Context, familyID, userID, role string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO family_members (id, family_id, user_id, role) VALUES (?,?,?,?)`,
		NewID(), familyID, userID, role)
	return err
}

// MemberRole returns "" when the user is not a member.
func (s *Store) MemberRole(ctx context.Context, familyID, userID string) (string, error) {
	var role string
	err := s.DB.QueryRowContext(ctx, `SELECT role FROM family_members WHERE family_id = ? AND user_id = ?`, familyID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return role, err
}

func (s *Store) FamiliesForUser(ctx context.Context, userID string) ([]Family, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT f.id, f.name, f.invite_code, f.created_by, f.created_at, m.role
		FROM families f JOIN family_members m ON m.family_id = f.id
		WHERE m.user_id = ? ORDER BY m.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Family{}
	for rows.Next() {
		var f Family
		var createdBy sql.NullString
		if err := rows.Scan(&f.ID, &f.Name, &f.InviteCode, &createdBy, &f.CreatedAt, &f.Role); err != nil {
			return nil, err
		}
		f.CreatedBy = strPtr(createdBy)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Members(ctx context.Context, familyID string) ([]Member, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id, u.name, u.email, m.role, m.created_at,
		(SELECT MAX(COALESCE(sh.ended_at, sh.started_at)) FROM shifts sh
		   JOIN care_profiles cp ON cp.id = sh.care_profile_id
		  WHERE sh.caregiver_id = u.id AND cp.family_id = m.family_id) AS last_shift
		FROM family_members m JOIN users u ON u.id = m.user_id
		WHERE m.family_id = ? ORDER BY m.created_at`, familyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var last sql.NullTime
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.JoinedAt, &last); err != nil {
			return nil, err
		}
		m.LastShiftAt = timePtr(last)
		m.OnDutyFor = []string{}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Giliran aktif per anggota.
	srows, err := s.DB.QueryContext(ctx, `SELECT sh.caregiver_id, sh.care_profile_id FROM shifts sh
		JOIN care_profiles cp ON cp.id = sh.care_profile_id
		WHERE cp.family_id = ? AND sh.ended_at IS NULL AND sh.caregiver_id IS NOT NULL`, familyID)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	idx := map[string]int{}
	for i, m := range out {
		idx[m.UserID] = i
	}
	for srows.Next() {
		var uid, pid string
		if err := srows.Scan(&uid, &pid); err != nil {
			return nil, err
		}
		if i, ok := idx[uid]; ok {
			out[i].OnDutyFor = append(out[i].OnDutyFor, pid)
		}
	}
	return out, srows.Err()
}

// ---------- profiles ----------

const profileCols = `id, family_id, name, nickname, profile_type, birth_date, notes, tracked_metrics, is_active, created_at`

func scanProfile(sc interface{ Scan(...any) error }) (*Profile, error) {
	var p Profile
	var nick, notes sql.NullString
	var birth sql.NullTime
	var metrics []byte
	if err := sc.Scan(&p.ID, &p.FamilyID, &p.Name, &nick, &p.ProfileType, &birth, &notes, &metrics, &p.IsActive, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.Nickname, p.Notes, p.BirthDate = strPtr(nick), strPtr(notes), datePtr(birth)
	p.TrackedMetrics = []string{}
	_ = json.Unmarshal(metrics, &p.TrackedMetrics)
	return &p, nil
}

func (s *Store) CreateProfile(ctx context.Context, p *Profile) error {
	p.ID = NewID()
	p.CreatedAt = time.Now().UTC()
	p.IsActive = true
	metrics, _ := json.Marshal(p.TrackedMetrics)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO care_profiles (`+profileCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.FamilyID, p.Name, nullStr(p.Nickname), p.ProfileType, nullStr(p.BirthDate), nullStr(p.Notes), metrics, p.IsActive, p.CreatedAt)
	return err
}

func (s *Store) UpdateProfile(ctx context.Context, p *Profile) error {
	metrics, _ := json.Marshal(p.TrackedMetrics)
	_, err := s.DB.ExecContext(ctx, `UPDATE care_profiles SET name=?, nickname=?, profile_type=?, birth_date=?, notes=?, tracked_metrics=?, is_active=? WHERE id=?`,
		p.Name, nullStr(p.Nickname), p.ProfileType, nullStr(p.BirthDate), nullStr(p.Notes), metrics, p.IsActive, p.ID)
	return err
}

func (s *Store) ProfileByID(ctx context.Context, id string) (*Profile, error) {
	p, err := scanProfile(s.DB.QueryRowContext(ctx, `SELECT `+profileCols+` FROM care_profiles WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return p, nil
}

func (s *Store) ProfilesByFamily(ctx context.Context, familyID string) ([]Profile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+profileCols+` FROM care_profiles WHERE family_id = ? AND is_active = 1 ORDER BY created_at`, familyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
