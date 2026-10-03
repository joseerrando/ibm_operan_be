-- Operan schema (MariaDB 10.4+ / MySQL 8+).
-- Penyesuaian untuk MariaDB/MySQL:
--   UUID  -> CHAR(36), dibuat di aplikasi
--   TEXT[]/TIME[]/JSONB -> JSON
--   TIMESTAMPTZ -> DATETIME(3), selalu UTC

CREATE TABLE users (
  id            CHAR(36)     NOT NULL PRIMARY KEY,
  name          VARCHAR(120) NOT NULL,
  email         VARCHAR(190) NOT NULL UNIQUE,
  password_hash VARCHAR(100) NOT NULL,
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE families (
  id          CHAR(36)     NOT NULL PRIMARY KEY,
  name        VARCHAR(120) NOT NULL,
  invite_code CHAR(7)      NOT NULL UNIQUE,
  created_by  CHAR(36)     NULL,
  created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_families_user FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE family_members (
  id         CHAR(36)    NOT NULL PRIMARY KEY,
  family_id  CHAR(36)    NOT NULL,
  user_id    CHAR(36)    NOT NULL,
  role       VARCHAR(16) NOT NULL CHECK (role IN ('admin','caregiver')),
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_family_user (family_id, user_id),
  CONSTRAINT fk_fm_family FOREIGN KEY (family_id) REFERENCES families(id) ON DELETE CASCADE,
  CONSTRAINT fk_fm_user   FOREIGN KEY (user_id)   REFERENCES users(id)    ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE care_profiles (
  id              CHAR(36)     NOT NULL PRIMARY KEY,
  family_id       CHAR(36)     NOT NULL,
  name            VARCHAR(120) NOT NULL,
  nickname        VARCHAR(60)  NULL,
  profile_type    VARCHAR(16)  NOT NULL CHECK (profile_type IN ('anak','lansia','pemulihan','kronis')),
  birth_date      DATE         NULL,
  notes           TEXT         NULL,
  tracked_metrics JSON         NOT NULL,
  is_active       TINYINT(1)   NOT NULL DEFAULT 1,
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_profiles_family (family_id),
  CONSTRAINT fk_profiles_family FOREIGN KEY (family_id) REFERENCES families(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE medications (
  id                   CHAR(36)     NOT NULL PRIMARY KEY,
  care_profile_id      CHAR(36)     NOT NULL,
  name                 VARCHAR(120) NOT NULL,
  kind                 VARCHAR(16)  NOT NULL CHECK (kind IN ('obat','vitamin','suplemen')),
  dose_label           VARCHAR(160) NOT NULL,
  schedule_times       JSON         NULL,          -- ["08:00","20:00"], NULL untuk bila perlu
  is_prn               TINYINT(1)   NOT NULL DEFAULT 0,
  min_interval_minutes INT          NULL,
  start_date           DATE         NULL,
  end_date             DATE         NULL,
  is_active            TINYINT(1)   NOT NULL DEFAULT 1,
  created_at           DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_meds_profile (care_profile_id),
  CONSTRAINT fk_meds_profile FOREIGN KEY (care_profile_id) REFERENCES care_profiles(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE medication_doses (
  id              CHAR(36)     NOT NULL PRIMARY KEY,
  medication_id   CHAR(36)     NOT NULL,
  scheduled_at    DATETIME(3)  NULL,
  given_at        DATETIME(3)  NULL,
  given_by        CHAR(36)     NULL,
  status          VARCHAR(16)  NOT NULL CHECK (status IN ('pending','given','skipped')),
  note            TEXT         NULL,
  forced          TINYINT(1)   NOT NULL DEFAULT 0,
  marked_at       DATETIME(3)  NULL,          -- kapan status terakhir diubah (untuk Batalkan 10 menit)
  missed_alerted  TINYINT(1)   NOT NULL DEFAULT 0,
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uq_dose_slot (medication_id, scheduled_at),
  KEY idx_doses_given (medication_id, given_at),
  KEY idx_doses_pending (status, scheduled_at),
  CONSTRAINT fk_doses_med  FOREIGN KEY (medication_id) REFERENCES medications(id) ON DELETE CASCADE,
  CONSTRAINT fk_doses_user FOREIGN KEY (given_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE care_logs (
  id              CHAR(36)    NOT NULL PRIMARY KEY,
  care_profile_id CHAR(36)    NOT NULL,
  author_id       CHAR(36)    NULL,
  log_type        VARCHAR(16) NOT NULL CHECK (log_type IN ('suhu','tensi','gula_darah','makan','tidur','bab_bak','keluhan','catatan')),
  value           JSON        NOT NULL,
  source          VARCHAR(8)  NOT NULL CHECK (source IN ('manual','voice')),
  recorded_at     DATETIME(3) NOT NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_logs_profile_time (care_profile_id, recorded_at),
  CONSTRAINT fk_logs_profile FOREIGN KEY (care_profile_id) REFERENCES care_profiles(id) ON DELETE CASCADE,
  CONSTRAINT fk_logs_user    FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE shifts (
  id              CHAR(36)    NOT NULL PRIMARY KEY,
  care_profile_id CHAR(36)    NOT NULL,
  caregiver_id    CHAR(36)    NULL,
  started_at      DATETIME(3) NOT NULL,
  ended_at        DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_shifts_profile (care_profile_id, started_at),
  CONSTRAINT fk_shifts_profile FOREIGN KEY (care_profile_id) REFERENCES care_profiles(id) ON DELETE CASCADE,
  CONSTRAINT fk_shifts_user    FOREIGN KEY (caregiver_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE handovers (
  id              CHAR(36)    NOT NULL PRIMARY KEY,
  care_profile_id CHAR(36)    NOT NULL,
  from_shift_id   CHAR(36)    NULL,
  from_user_id    CHAR(36)    NULL,
  to_user_id      CHAR(36)    NULL,
  summary         TEXT        NOT NULL,
  pending_items   JSON        NULL,
  watch_items     JSON        NULL,
  ai_generated    TINYINT(1)  NOT NULL DEFAULT 0,
  read_at         DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_handovers_profile (care_profile_id, created_at),
  CONSTRAINT fk_ho_profile FOREIGN KEY (care_profile_id) REFERENCES care_profiles(id) ON DELETE CASCADE,
  CONSTRAINT fk_ho_shift   FOREIGN KEY (from_shift_id) REFERENCES shifts(id) ON DELETE SET NULL,
  CONSTRAINT fk_ho_from    FOREIGN KEY (from_user_id) REFERENCES users(id) ON DELETE SET NULL,
  CONSTRAINT fk_ho_to      FOREIGN KEY (to_user_id) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE alerts (
  id              CHAR(36)     NOT NULL PRIMARY KEY,
  care_profile_id CHAR(36)     NOT NULL,
  source          VARCHAR(8)   NOT NULL CHECK (source IN ('rule','ai')),
  severity        VARCHAR(16)  NOT NULL CHECK (severity IN ('info','perhatian','penting')),
  title           VARCHAR(200) NOT NULL,
  message         TEXT         NOT NULL,
  metric          VARCHAR(16)  NULL,
  is_read         TINYINT(1)   NOT NULL DEFAULT 0,
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_alerts_profile (care_profile_id, is_read, created_at),
  CONSTRAINT fk_alerts_profile FOREIGN KEY (care_profile_id) REFERENCES care_profiles(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE doctor_reports (
  id              CHAR(36)    NOT NULL PRIMARY KEY,
  care_profile_id CHAR(36)    NOT NULL,
  period_start    DATETIME(3) NOT NULL,
  period_end      DATETIME(3) NOT NULL,
  content         JSON        NOT NULL,
  created_by      CHAR(36)    NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_reports_profile (care_profile_id, created_at),
  CONSTRAINT fk_reports_profile FOREIGN KEY (care_profile_id) REFERENCES care_profiles(id) ON DELETE CASCADE,
  CONSTRAINT fk_reports_user    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE device_tokens (
  id         CHAR(36)     NOT NULL PRIMARY KEY,
  user_id    CHAR(36)     NOT NULL,
  fcm_token  VARCHAR(255) NOT NULL UNIQUE,
  platform   VARCHAR(8)   NULL CHECK (platform IN ('android','web')),
  updated_at DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT fk_tokens_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
