-- Client-Protokoll: Start/Stop des Windows-Symbols (nur Administration, mit Aufbewahrungsfrist)
CREATE TABLE IF NOT EXISTS client_events (
  id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  at         DATETIME(3)  NOT NULL,
  client_at  DATETIME(3)  NULL,
  event      VARCHAR(10)  NOT NULL,
  reason     VARCHAR(20)  NOT NULL DEFAULT '',
  hostname   VARCHAR(64)  NOT NULL,
  username   VARCHAR(128) NOT NULL DEFAULT '',
  client_ips VARCHAR(400) NOT NULL DEFAULT '',
  remote_ip  VARCHAR(45)  NOT NULL DEFAULT '',
  version    VARCHAR(20)  NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_client_events_host (hostname, id),
  KEY idx_client_events_at (at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
