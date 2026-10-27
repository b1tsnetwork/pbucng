# Telefonbuch 1.0.0-rc.1 – Release Candidate mit Kommunikation (UCNG)

**Datum:** 27.10.2026 · **Art:** Vorabversion für Abnahme und Test
**Passende UCNG-Fassung:** `v1.0.0-rc.1` (Branch `phonebook-integration`)

> **Nicht für den Produktivbetrieb freigegeben**, solange die Lizenz- und Markenprüfung (ADR-016, Gate E) fehlt.

## Neu seit dem letzten Stand (PH-05)

**Profilbild**
- Das Pixel-Avatar aus dem Telefonbuch ist das Startbild in der Kommunikation.
- Man kann es dort durch ein eigenes Bild ersetzen. Entfernt man das eigene Bild, kommt das Pixel-Avatar zurück.
- Ändert man das Avatar im Telefonbuch, ändert es sich in der Kommunikation mit (Standard: innerhalb von 5 min), solange kein eigenes Bild gesetzt ist.

**Funktionen im Zusammenspiel geprüft**
- Sprachkanäle, auch Ende-zu-Ende-verschlüsselt (gleicher Sicherheitscode bei allen).
- Bühne, Handzeichen, Moderation, Verschieben, Breakout-Räume.
- Aufnahme im Browser mit Anzeige bei allen.
- Abgesetzte Sprach-Server.
- Temporäre Kanäle, Bots, Moderation, Automoderation.
- Ankündigungen, Statistik, Vorlagen, Sprachen.
- Daten einer Person löschen, Sicherung und Wiederherstellung.

**Sicherheit**
- Keine Verbindungen zu GIPHY, Tenor oder einem Beacon; die eigene GIF-Bibliothek und Emotes bleiben.
- Kein Widget für fremde Webseiten.
- Schutz vor Anfragen ins interne Netz (Downloads im Admin-Bereich, Webhooks).
- Transkripte nur mit Frist (Standard 14 Tage). Die Spracherkennung bleibt aus, bis sie freigegeben ist.
- AD-Nachprüfung im Telefonbuch selbst: Wer im AD gesperrt, gelöscht oder aus der Anmeldegruppe entfernt wird, verliert innerhalb von 15 min alle Sitzungen, auch in der Kommunikation.

**Installation und Betrieb**
- `deploy/stack/`: Komplett-Installation mit Gateway (Caddy, drei TLS-Arten), MySQL, Telefonbuch, Kommunikation, Datenbank-Sicherung, optional Spracherkennung, Prüfskript.
- `deploy/stack/voice-node/`: abgesetzte Sprach-Server.
- Alle Container laufen ohne Root, schreibgeschützt und ohne Capabilities. Geheimnisse liegen nur als Dateien vor.
- **Ein Repository:** Telefonbuch (`phonebook/`), Kommunikation (`ucng/`) und Installation (`deploy/stack/`) liegen zusammen. Gemeinsame Workflows im Wurzelordner:
  - `ci.yml`: Tests beider Teile, `govulncheck`;
  - `release.yml`: fünf Images, Programmdateien, Installationspaket, Prüfsummen;
  - `stack.yml`: baut alle Images aus dem Stand und startet den Stack.
- `build-images.sh`: baut alle Images und schiebt sie in eine Registry.

## Komponenten

| Komponente | Image |
|---|---|
| Telefonbuch | `ghcr.io/b1tsnetwork/pbucng/phonebook:1.0.0-rc.1` |
| Kommunikation | `ghcr.io/b1tsnetwork/pbucng/ucng:1.0.0-rc.1` |
| Sprach-Server | `ghcr.io/b1tsnetwork/pbucng/ucng-voice:1.0.0-rc.1` |
| Datenbank | `mysql:8.4` |
| Gateway | `caddy:2.10` |

## Hinweise zum Umstieg

- **Datenbank:** Migration `011_comm_avatars.sql` läuft beim Start automatisch.
- **Neue Einstellung `auth.adRecheck`** (Standard `15m`, 1 min bis 24 h): AD-Sitzungen werden nachgeprüft, eine geänderte Admin-Berechtigung erzwingt eine neue Anmeldung.
- **Neue Umgebungsvariablen:** `TB_PUBLIC_URL`, `TB_TRUSTED_PROXIES`, `TB_METRICS_TOKEN_FILE`, `TB_CLIENT_LOG_KEY_FILE`.
- **UCNG im Modus `phonebook`:**
  - Bestehende Widgets funktionieren nicht mehr.
  - Externe GIF-Dienste und der Beacon sind gesperrt.
  - `TRANSCRIPT_KEEP_DAYS` über 365 verhindert den Start; ohne Angabe gelten 14 Tage.
- **UCNG `PHONEBOOK_INTERNAL_URL`:** `http://app:8080` (Dienstname ohne Punkt) ist jetzt erlaubt. Vorher wurde es abgelehnt, und die Anleitung aus PH-01 startete nicht.
- **`.gitignore`:** Muster wie `config.json` gelten nur noch im Wurzelordner.

## Tests

| Was | Ergebnis |
|---|---|
| Telefonbuch | gofmt, vet, `go test -race` mit MySQL-Ersatz: alle bestanden |
| UCNG | gofmt, vet, `go test -race`: alle bestanden (4 übersprungen wie bisher: SQLite, QR-Export); Build `-tags mysql` |
| Browser (Chromium), echte Prozesse | ph02 32/32, ph03 18/18, ph04 27/27, ph05 20/20, ph06 24/24 (neu: Profilbild, Sprache E2EE, Handzeichen, Aufnahme) |
| Stack | alle Dienste gesund mit allen Härtungen; `commcheck` durch den Gateway 17/17; Telefonbuch allein; abgesetzter Sprach-Server meldet sich an |

**Nicht ausgeführt:**
- Echte Images, Caddy und TLS. Die Registries waren gesperrt; ersatzweise liefen Platzhalter-Images aus denselben Programmen. Der Workflow `stack.yml` holt das nach.
- `govulncheck`: läuft in CI.
- Echtes AD.
- Firefox, Edge, Safari, echte Geräte und Bildschirmfreigabe.
- UCNG mit SQLite.

## Offen

- **ADR-016 Lizenz und Marke:** Rechtsprüfung vor jedem Produktivbetrieb.
- **ADR-011:** organisatorische Freigabe der Transkription.
- **Abnahme vor Ort:** siehe [`docs/security/ph11-review.md`](docs/security/ph11-review.md) §4.

Entscheidungen dieses Release Candidate: [`docs/RELEASE-ENTSCHEIDUNGEN.md`](docs/RELEASE-ENTSCHEIDUNGEN.md).
