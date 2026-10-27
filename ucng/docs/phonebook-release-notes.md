# UCNG `phonebook-integration` 1.0.0-rc.1 – Release Candidate

**Datum:** 27.10.2026 · Vorabversion für Abnahme und Test · **passendes Telefonbuch:** `v1.0.0-rc.1`

Ohne `MODE=phonebook` verhält sich UCNG wie bisher. Jede Änderung ist darauf geprüft (Tests mit „Standalone“ bzw. „Unchanged“ im Namen).

## Modus `phonebook` (seit PH-01)

**Anmeldung und Konten**
- Anmeldung nur über das Telefonbuch: Authorization Code + PKCE, Schattenkonten ohne Passwort.
- Regelmäßige Nachprüfung der Sitzungen; Austritt sperrt das Konto und anonymisiert es nach einer Frist.

**Verschlüsselte Direktnachrichten**
- Mit eigenem Entsperr-Geheimnis und Wiederherstellungscode.

**Organisation und Rechte**
- Organisationskanäle; Admin- und Moderatorzuordnung aus dem Telefonbuch.

**Navigation und Zusammenspiel**
- Rücksprung ins Telefonbuch, Design-Adapter, 503 bei Ausfall des Telefonbuchs.
- Direktnachricht aus der Visitenkarte, Zähler „neu seit letztem Besuch“.

**Sperren**
- Sechs Betriebssperren werden erzwungen: Gäste, Registrierung, Föderation, Kanal-Föderation, Beacon, externe GIFs.

## Neu im Release Candidate

**Profilbild**
- Das Telefonbuch-Avatar ist das Startbild (`phonebook_avatar.go`).
- Ein eigenes Bild hat Vorrang. Entfernen holt das Startbild zurück.

**Betriebssperren** (`oplocks.go`)
- `LOCK_BEACON` und `LOCK_EXTERNAL_GIFS` sind neu. Gesperrt sind GIF-Suche bei GIPHY/Tenor, Beacon-Sammlungen und Sprachpakete aus dem Beacon.

**Widget**
- Im Modus `phonebook` gibt es kein Widget: Anlegen/Ändern → 403, Vorlagen mit Widgets → abgelehnt.
- CORS nur mit `ALLOWED_ORIGINS`.

**SSRF-Schutz** (`netguard.go`)
- Gilt für Downloads aus dem Admin-Bereich und für Webhooks.
- Private Netze bei Downloads nur mit `OUTBOUND_ALLOW_PRIVATE=1`.

**Transkripte**
- Standard-Frist 14 Tage, mehr als 365 Tage → kein Start.
- `/api/settings` meldet Frist und STT.

**Interne Adresse**
- `PHONEBOOK_INTERNAL_URL` darf `http` mit einem Dienstnamen ohne Punkt nutzen (Docker-Netz).

**Betrieb**
- `<NAME>_FILE` für alle Geheimnisse, z. B. `ADMIN_PASSWORD_FILE`, `VOICE_AGENT_TOKEN_FILE`, `AGENT_TOKEN_FILE`.
- `ucng healthcheck` und `ucng-voice healthcheck`; `HEALTHCHECK` in `Dockerfile` und `Dockerfile.voice`.
- `release.yml`: Tags mit Bindestrich werden als Vorabversion veröffentlicht.

**Tests**
- `phonebook_ops_test.go`: Medien, Sperren, Sprache, Bühne, Sprach-Server, temporäre Kanäle, Bots, Moderation, Datenschutz, Admin.
- `phonebook_backup_test.go`: Sicherung und Wiederherstellung.
- `netguard_test.go`, `envfile_test.go`, `phonebook_avatar_test.go`.

**Installation:** Referenz-Installation im Gesamt-Repository unter `deploy/stack/` (mit `voice-node/` für abgesetzte Sprach-Server).

## Vor dem Produktivbetrieb

**AGPL §13 (ADR-016)**
- Wer diese geänderte Fassung Personen über das Netz anbietet, auch Beschäftigten, muss ihnen den Quellcode anbieten.
- Die Release-Images setzen `SOURCE_URL` auf Repository und Commit. Dieses Repository muss für die Nutzerinnen und Nutzer erreichbar sein.
- Die Rechtsprüfung steht aus.
