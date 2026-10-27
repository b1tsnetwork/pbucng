-- Kommunikations-Add-on (PH-06, ADR-015): Pixel-Avatar als Startbild in der Kommunikation.
-- Das PNG rendert der Browser der Person aus ihrem Avatar; der Server prüft und speichert es neu.
CREATE TABLE IF NOT EXISTS comm_avatars (
  principal_id VARCHAR(40)  NOT NULL,
  source       VARCHAR(64)  NOT NULL,
  hash         VARCHAR(64)  NOT NULL,
  png          MEDIUMBLOB   NOT NULL,
  updated_at   DATETIME(3)  NOT NULL,
  PRIMARY KEY (principal_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
