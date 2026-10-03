package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"operan-be/internal/apperr"
	"operan-be/internal/db"
)

const tokenTTL = 30 * 24 * time.Hour

type AuthResult struct {
	Token string   `json:"token"`
	User  *db.User `json:"user"`
}

func (s *Service) Register(ctx context.Context, name, email, password string) (*AuthResult, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(strings.ToLower(email))
	if name == "" {
		return nil, apperr.Validation("Nama wajib diisi.")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, apperr.Validation("Format email belum benar.")
	}
	if len(password) < 8 {
		return nil, apperr.Validation("Kata sandi minimal 8 karakter.")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u, err := s.Store.CreateUser(ctx, name, email, string(hash))
	if db.IsDuplicate(err) {
		return nil, apperr.Conflict("EMAIL_TAKEN", "Email ini sudah terdaftar. Coba masuk.")
	}
	if err != nil {
		return nil, err
	}
	return s.issue(u)
}

func (s *Service) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	u, err := s.Store.UserByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, db.ErrNotFound) {
		return nil, apperr.Unauthorized("Email atau kata sandi belum cocok.")
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, apperr.Unauthorized("Email atau kata sandi belum cocok.")
	}
	return s.issue(u)
}

func (s *Service) issue(u *db.User) (*AuthResult, error) {
	now := s.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: u.ID, Audience: jwt.ClaimStrings{"operan-app"},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
	})
	signed, err := tok.SignedString(s.JWTSecret)
	if err != nil {
		return nil, err
	}
	return &AuthResult{Token: signed, User: u}, nil
}

// ParseToken returns the user ID inside a valid app token.
func (s *Service) ParseToken(raw string) (string, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) { return s.JWTSecret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience("operan-app"))
	if err != nil || claims.Subject == "" {
		return "", apperr.Unauthorized("Sesi berakhir. Silakan masuk lagi.")
	}
	return claims.Subject, nil
}

type MeResult struct {
	User     *db.User    `json:"user"`
	Families []db.Family `json:"families"`
}

func (s *Service) Me(ctx context.Context, userID string) (*MeResult, error) {
	u, err := s.Store.UserByID(ctx, userID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apperr.Unauthorized("Akun tidak ditemukan. Silakan masuk lagi.")
	}
	if err != nil {
		return nil, err
	}
	fams, err := s.Store.FamiliesForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &MeResult{User: u, Families: fams}, nil
}

func (s *Service) RegisterDevice(ctx context.Context, userID, token, platform string) error {
	if strings.TrimSpace(token) == "" {
		return apperr.Validation("Token perangkat kosong.")
	}
	if platform != "android" && platform != "web" {
		return apperr.Validation("Platform harus android atau web.")
	}
	return s.Store.UpsertDeviceToken(ctx, userID, token, platform)
}
