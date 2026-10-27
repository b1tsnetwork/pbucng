-- Kommunikations-Add-on (PH-02): stabile Identität (principal_id) und Bezug der Sitzung dazu
CREATE TABLE IF NOT EXISTS principals (
  id           VARCHAR(40)  NOT NULL,
  provider     VARCHAR(10)  NOT NULL,
  subject      VARCHAR(160) NOT NULL,
  username     VARCHAR(128) NOT NULL,
  display_name VARCHAR(200) NOT NULL DEFAULT '',
  groups_json  TEXT         NULL,
  is_admin     TINYINT(1)   NOT NULL DEFAULT 0,
  disabled     TINYINT(1)   NOT NULL DEFAULT 0,
  created_at   DATETIME(3)  NOT NULL,
  checked_at   DATETIME(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_principals_subject (provider, subject),
  KEY idx_principals_user (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE sessions ADD COLUMN principal_id VARCHAR(40) NOT NULL DEFAULT '';
