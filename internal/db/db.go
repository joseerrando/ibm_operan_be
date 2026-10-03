// Package db holds the MariaDB/MySQL connection, migrations, and all queries.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"operan-be/migrations"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
}

// Open connects with UTC session settings so every DATETIME is read/written as UTC.
func Open(dsn string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.Params["charset"] = "utf8mb4"

	conn, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(20)
	conn.SetMaxIdleConns(10)
	conn.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{DB: conn}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Migrate applies embedded *.sql files in name order, once each.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(100) NOT NULL PRIMARY KEY,
		applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3))`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var exists int
		err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			return err
		}
	}
	return nil
}

// Reset drops every table. Only used by the seed command and tests.
func (s *Store) Reset(ctx context.Context) error {
	tables := []string{"device_tokens", "doctor_reports", "alerts", "handovers", "shifts", "care_logs",
		"medication_doses", "medications", "care_profiles", "family_members", "families", "users", "schema_migrations"}
	_, err := s.DB.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0; DROP TABLE IF EXISTS "+strings.Join(tables, ", ")+"; SET FOREIGN_KEY_CHECKS=1;")
	return err
}

func NewID() string { return uuid.NewString() }

func isDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// IsDuplicate reports whether err is a unique-key violation.
func IsDuplicate(err error) bool { return isDuplicate(err) }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func nullStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func timePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	v := nt.Time.UTC()
	return &v
}

func datePtr(nt sql.NullTime) *string {
	if !nt.Valid {
		return nil
	}
	v := nt.Time.Format("2006-01-02")
	return &v
}
