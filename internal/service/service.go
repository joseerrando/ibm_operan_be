package service

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
	"operan-be/internal/langflow"
	"operan-be/internal/sse"
	"operan-be/internal/stt"
)

type Service struct {
	Store *db.Store
	Hub   *sse.Hub
	AI    langflow.Runner
	// Fallback dipakai saat model gagal (timeout, 503): ekstraksi dan jawaban deterministik dari data.
	Fallback langflow.Runner
	STT      stt.Transcriber
	Loc      *time.Location
	Now      func() time.Time

	JWTSecret     []byte
	PublicBaseURL string

	trendMu   sync.Mutex
	trendLast map[string]time.Time

	toolMu       sync.Mutex
	toolSessions map[string]toolSession
}

type toolSession struct {
	ProfileID string
	Expires   time.Time
}

func New(store *db.Store, hub *sse.Hub, ai langflow.Runner, tr stt.Transcriber, loc *time.Location, jwtSecret, publicBaseURL string) *Service {
	return &Service{
		Store: store, Hub: hub, AI: ai, Fallback: langflow.NewMockRunner(loc), STT: tr, Loc: loc, Now: time.Now,
		JWTSecret: []byte(jwtSecret), PublicBaseURL: publicBaseURL,
		trendLast: map[string]time.Time{}, toolSessions: map[string]toolSession{},
	}
}

func (s *Service) now() time.Time { return s.Now().UTC() }

// ---------- authorization ----------

// RequireFamily returns the caller's role, or 403 when they are not a member.
func (s *Service) RequireFamily(ctx context.Context, familyID, userID string) (string, error) {
	role, err := s.Store.MemberRole(ctx, familyID, userID)
	if err != nil {
		return "", err
	}
	if role == "" {
		if _, err := s.Store.FamilyByID(ctx, familyID); errors.Is(err, db.ErrNotFound) {
			return "", apperr.NotFound("Keluarga tidak ditemukan.")
		}
		return "", apperr.Forbidden()
	}
	return role, nil
}

// RequireProfile loads a care profile and checks the caller belongs to its family.
func (s *Service) RequireProfile(ctx context.Context, profileID, userID string) (*db.Profile, error) {
	p, err := s.Store.ProfileByID(ctx, profileID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apperr.NotFound("Orang yang dirawat tidak ditemukan.")
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.RequireFamily(ctx, p.FamilyID, userID); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) RequireMedication(ctx context.Context, medID, userID string) (*db.Medication, *db.Profile, error) {
	m, err := s.Store.MedicationByID(ctx, medID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil, apperr.NotFound("Obat tidak ditemukan.")
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.RequireProfile(ctx, m.CareProfileID, userID)
	if err != nil {
		return nil, nil, err
	}
	return m, p, nil
}

func (s *Service) RequireDose(ctx context.Context, doseID, userID string) (*db.Dose, *db.Medication, *db.Profile, error) {
	d, err := s.Store.DoseByID(ctx, doseID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil, nil, apperr.NotFound("Jadwal obat tidak ditemukan.")
	}
	if err != nil {
		return nil, nil, nil, err
	}
	m, p, err := s.RequireMedication(ctx, d.MedicationID, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	return d, m, p, nil
}

// ---------- events ----------

func (s *Service) emit(familyID, typ, profileID, actorID string, data any) {
	if s.Hub == nil {
		return
	}
	s.Hub.Publish(familyID, sse.Event{Type: typ, ProfileID: profileID, ActorID: actorID, Data: data})
}

// ---------- small helpers ----------

// Invite codes: XXX-XXX without look-alike characters (O/0, I/1, L).
const inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func NewInviteCode() string {
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(inviteAlphabet))))
		b[i] = inviteAlphabet[n.Int64()]
	}
	return string(b[:3]) + "-" + string(b[3:])
}

// NormalizeInviteCode accepts "k7p42q", "K7P 42Q", "K7P-42Q".
func NormalizeInviteCode(in string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		if strings.ContainsRune(inviteAlphabet, r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) != 6 {
		return s
	}
	return s[:3] + "-" + s[3:]
}

func (s *Service) localISO(t time.Time) string { return t.In(s.Loc).Format(time.RFC3339) }

func (s *Service) logErr(msg string, err error, args ...any) {
	if err != nil {
		slog.Error(msg, append(args, "err", err)...)
	}
}
