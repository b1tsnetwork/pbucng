# Geheimnisse

`sh secrets/create.sh` (als root) legt alle Dateien an. Sie werden als Docker-Secrets in die
Container eingebunden und stehen nie in der Umgebung oder in `.env`.

| Datei | Wofür | Wer liest |
|---|---|---|
| `db_password` | MySQL-Benutzer `telefonbuch` | MySQL, Telefonbuch |
| `db_root_password` | MySQL root (Sicherung) | MySQL, db-backup |
| `ldap_bind_password` | Dienstkonto für LDAPS/AD | Telefonbuch |
| `metrics_token` | `/metrics` des Telefonbuchs (Bearer) | Telefonbuch |
| `ucng_integration_secret` | gemeinsames Geheimnis Telefonbuch ↔ Kommunikation (≥ 32 Zeichen) | Telefonbuch, Kommunikation |
| `ucng_admin_password` | Notfallzugang `/admin` der Kommunikation | Kommunikation |
| `voice_agent_token` | Schlüssel zwischen Kommunikation und abgesetzten Sprach-Servern | Kommunikation, voice-node |

Rechte: Besitzer root, Gruppe 65532, Modus 0440. Die Dateien gehören in keine Datensicherung,
die unverschlüsselt außer Haus geht, und nicht ins Git (`.gitignore`).
