package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
	"operan-be/internal/sse"
)

func (s *Service) CreateFamily(ctx context.Context, userID, name string) (*db.Family, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apperr.Validation("Nama keluarga wajib diisi.")
	}
	for attempt := 0; attempt < 5; attempt++ {
		f, err := s.Store.CreateFamily(ctx, name, NewInviteCode(), userID)
		if db.IsDuplicate(err) {
			continue // kode undangan bentrok, coba lagi
		}
		return f, err
	}
	return nil, apperr.Internal()
}

func (s *Service) JoinFamily(ctx context.Context, userID, code string) (*db.Family, error) {
	f, err := s.Store.FamilyByInviteCode(ctx, NormalizeInviteCode(code))
	if errors.Is(err, db.ErrNotFound) {
		return nil, apperr.NotFound("Kode undangan tidak ditemukan. Periksa lagi 6 karakternya.")
	}
	if err != nil {
		return nil, err
	}
	if err := s.Store.AddMember(ctx, f.ID, userID, "caregiver"); err != nil {
		return nil, err
	}
	role, _ := s.Store.MemberRole(ctx, f.ID, userID)
	f.Role = role
	s.emit(f.ID, "member_joined", "", userID, nil)
	return f, nil
}

func (s *Service) Family(ctx context.Context, familyID, userID string) (*db.Family, error) {
	role, err := s.RequireFamily(ctx, familyID, userID)
	if err != nil {
		return nil, err
	}
	f, err := s.Store.FamilyByID(ctx, familyID)
	if err != nil {
		return nil, err
	}
	f.Role = role
	return f, nil
}

func (s *Service) Members(ctx context.Context, familyID, userID string) ([]db.Member, error) {
	if _, err := s.RequireFamily(ctx, familyID, userID); err != nil {
		return nil, err
	}
	return s.Store.Members(ctx, familyID)
}

// ---------- profiles ----------

var profileTypes = map[string]bool{"anak": true, "lansia": true, "pemulihan": true, "kronis": true}

var metricTypes = map[string]bool{"suhu": true, "tensi": true, "gula_darah": true, "makan": true, "tidur": true,
	"bab_bak": true, "keluhan": true}

// DefaultMetrics returns the metrics tracked by default for a profile type.
func DefaultMetrics(profileType string) []string {
	switch profileType {
	case "anak", "pemulihan":
		return []string{"suhu", "makan", "tidur", "keluhan"}
	default:
		return []string{"tensi", "gula_darah", "makan", "keluhan"}
	}
}

type ProfileInput struct {
	Name           *string   `json:"name"`
	Nickname       *string   `json:"nickname"`
	ProfileType    *string   `json:"profile_type"`
	BirthDate      *string   `json:"birth_date"`
	Notes          *string   `json:"notes"`
	TrackedMetrics *[]string `json:"tracked_metrics"`
	IsActive       *bool     `json:"is_active"`
}

func (in ProfileInput) apply(p *db.Profile, creating bool) error {
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.Nickname != nil {
		v := strings.TrimSpace(*in.Nickname)
		p.Nickname = &v
		if v == "" {
			p.Nickname = nil
		}
	}
	if in.ProfileType != nil {
		p.ProfileType = *in.ProfileType
	}
	if in.BirthDate != nil {
		v := strings.TrimSpace(*in.BirthDate)
		if v == "" {
			p.BirthDate = nil
		} else if _, err := time.Parse("2006-01-02", v); err != nil {
			return apperr.Validation("Tanggal lahir harus berformat YYYY-MM-DD.")
		} else {
			p.BirthDate = &v
		}
	}
	if in.Notes != nil {
		v := strings.TrimSpace(*in.Notes)
		p.Notes = &v
		if v == "" {
			p.Notes = nil
		}
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if p.Name == "" && p.Nickname != nil {
		p.Name = *p.Nickname
	}
	if p.Name == "" {
		return apperr.Validation("Nama orang yang dirawat wajib diisi.")
	}
	if !profileTypes[p.ProfileType] {
		return apperr.Validation("Jenis perawatan harus anak, lansia, pemulihan, atau kronis.")
	}
	if in.TrackedMetrics != nil {
		var ms []string
		seen := map[string]bool{}
		for _, m := range *in.TrackedMetrics {
			if !metricTypes[m] {
				return apperr.Validation("Metrik " + m + " tidak dikenal.")
			}
			if !seen[m] {
				seen[m] = true
				ms = append(ms, m)
			}
		}
		p.TrackedMetrics = ms
	}
	if creating && len(p.TrackedMetrics) == 0 {
		p.TrackedMetrics = DefaultMetrics(p.ProfileType)
	}
	if p.TrackedMetrics == nil {
		p.TrackedMetrics = []string{}
	}
	return nil
}

func (s *Service) CreateProfile(ctx context.Context, familyID, userID string, in ProfileInput) (*db.Profile, error) {
	if _, err := s.RequireFamily(ctx, familyID, userID); err != nil {
		return nil, err
	}
	p := &db.Profile{FamilyID: familyID}
	if err := in.apply(p, true); err != nil {
		return nil, err
	}
	if err := s.Store.CreateProfile(ctx, p); err != nil {
		return nil, err
	}
	s.emit(familyID, sse.ProfileChanged, p.ID, userID, nil)
	return p, nil
}

func (s *Service) UpdateProfile(ctx context.Context, profileID, userID string, in ProfileInput) (*db.Profile, error) {
	p, err := s.RequireProfile(ctx, profileID, userID)
	if err != nil {
		return nil, err
	}
	if err := in.apply(p, false); err != nil {
		return nil, err
	}
	if err := s.Store.UpdateProfile(ctx, p); err != nil {
		return nil, err
	}
	s.emit(p.FamilyID, sse.ProfileChanged, p.ID, userID, nil)
	return p, nil
}

func (s *Service) Profiles(ctx context.Context, familyID, userID string) ([]ProfileSummary, error) {
	if _, err := s.RequireFamily(ctx, familyID, userID); err != nil {
		return nil, err
	}
	ps, err := s.Store.ProfilesByFamily(ctx, familyID)
	if err != nil {
		return nil, err
	}
	out := make([]ProfileSummary, 0, len(ps))
	for _, p := range ps {
		alerts, err := s.Store.Alerts(ctx, p.ID, true, 20)
		if err != nil {
			return nil, err
		}
		sum := ProfileSummary{Profile: p}
		for _, a := range alerts {
			if a.Severity != "info" {
				sum.NeedsAttention = true
			}
		}
		out = append(out, sum)
	}
	return out, nil
}

// ProfileSummary adds the apricot-dot flag used by the person switcher.
type ProfileSummary struct {
	db.Profile
	NeedsAttention bool `json:"needs_attention"`
}
