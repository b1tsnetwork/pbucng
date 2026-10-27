# PH-11 – Sicherheitsprüfung des Release Candidate 1.0.0-rc.1

**Stand:** 27.10.2026 · Telefonbuch `v1.0.0-rc.1` und UCNG `phonebook-integration` `v1.0.0-rc.1`
**Bezug:** [Bedrohungsmodell](ph02-threat-model.md) (T1–T47), [Entscheidungen des RC](../RELEASE-ENTSCHEIDUNGEN.md), ADR-009/-011/-013/-016/-027…-030

Diese Prüfung hat der Entwickler der Änderungen durchgeführt, kein unabhängiges Team. Vor dem Produktivbetrieb empfehle ich eine zweite, unabhängige Prüfung. Gate E (Lizenz, ADR-016) ist ohnehin noch offen.

## 1. Umfang

- **Angriffsfläche von außen:** Gateway (Telefonbuch-Domain, Kommunikations-Domain, Sprach-Server)
- **Server-zu-Server:** Schlüssel je Richtung, interne Pfade, Adressen
- **Anmeldung und Sitzungen:** SSO, Nachprüfung, AD-Nachprüfung, Ausfallverhalten
- **Ausgehende Verbindungen:** Medien, Webhooks, externe Dienste
- **Uploads:** Startbild, Profilbild
- **Betrieb:** Container, Geheimnisse, Proxy-Vertrauen, TLS, Sicherungen
- **Lieferkette:** Workflows, Versionen, Schwachstellenprüfung

## 2. Ergebnisse

| Bereich | Prüfung | Ergebnis |
|---|---|---|
| Gastzugang | `commcheck` durch den Gateway des Stacks: Kanalliste, Verlauf, Live-Stream, Profile ohne Anmeldung → 401; Gastname → 403; Registrierung → 404 | ✓ 17/17 Prüfungen, 0 Fehler (Stack-Lauf mit Platzhalter-Images) |
| Sicherheitsprofil | `hosted`, Gäste `none`, keine Registrierung/Föderation; alle sechs Betriebssperren | ✓ `commcheck`, `TestCommunicationProbe`, `TestPhonebookMediaAndLocks` |
| Einbettung | `frame-ancestors` gesetzt (ADR-019) | ✓ `commcheck`, `TestPhonebookFraming` |
| Live-Verbindungen | SSE ohne Proxy-Puffer (erste Daten nach 0 ms), WebSocket-Upgrade durch den Gateway | ✓ `commcheck` |
| Interne Pfade | `/api/internal/*` und `…/phonebook/{revoke,org,news}` brauchen den Schlüssel der richtigen Richtung; zusätzlich sperrt der Caddy-Gateway sie nach außen | ✓ Unit-Tests; ⚠ Caddy-Regel nicht ausgeführt (siehe 4) |
| Interne Adressen | http nur für Dienstnamen ohne Punkt (ADR-027) | ✓ `TestPhonebookConfig`, Mutationstest |
| SSRF | Loopback, Link-Local (169.254.169.254), Multicast immer gesperrt; private Netze für Downloads gesperrt; Prüfung nach DNS und bei Weiterleitung | ✓ `TestBlockedAddr`, `TestPhonebookOutboundGuard` |
| Andere ausgehende Verbindungen | Beacon und Föderation gesperrt; STT nur vom Betrieb konfiguriert; `/control` der Sprach-Server nur für angemeldete Agenten (Schlüssel) | ✓ Code-Durchsicht, `TestPhonebookVoiceAgents` |
| Startbild-Upload | nur PNG 128 × 128, neu kodiert, Metadaten weg; nur eigenes Konto; CSRF | ✓ `TestCommAvatar` |
| AD-Nachprüfung | Sperre, Löschung, fehlende Gruppe → alle Sitzungen weg; AD-Ausfall fail-open (bewusst) | ✓ `TestADRecheckSessions` |
| Ausfall Telefonbuch | UCNG antwortet 503 (auch `…/phonebook/user`); Sitzung gilt danach ohne neue Anmeldung wieder | ✓ `TestPhonebookRevalidate`, Browser ph02.js |
| E2EE Sprache | Schlüssel nur im Browser, gleicher Sicherheitscode bei allen, Ton kommt entschlüsselt an | ✓ Browser ph06.js |
| E2EE Direktnachrichten | eigenes Entsperr-Geheimnis, Wiederherstellungscode | ✓ Browser ph02.js, ph05.js |
| Container | ohne Root, schreibgeschützt, keine Capabilities, `no-new-privileges`; Geheimnisse als Dateien `0440 root:65532` | ✓ Stack-Lauf: alle Dienste gesund und Geheimnisse lesbar |
| Proxy-Vertrauen | nur die feste Gateway-Adresse (`/32`) | ✓ Konfiguration; Befund behoben (siehe 3) |
| Geheimnisse in der Umgebung | keine; `*_FILE` für alle UCNG-Geheimnisse | ✓ `TestEnvFile`, `docker compose config` |
| Lieferkette | feste Image-Versionen; Vorabversionen ohne `latest`; SBOM und Herkunftsnachweis (Telefonbuch-Image); `govulncheck` in beiden CIs | ✓ Workflows; ⚠ `govulncheck` hier nicht ausführbar |

## 3. Befunde und Behebung

| Nr. | Befund | Schwere | Behebung |
|---|---|---|---|
| F1 | Admins (auch aus dem Telefonbuch zugeordnet) konnten per „Emote/GIF per Adresse“ und Webhook interne Adressen abrufen (SSRF) | hoch | ADR-029, `netguard.go` |
| F2 | Externe GIF-Dienste und Beacon waren im Modus `phonebook` erreichbar | mittel | ADR-009 abgeschlossen |
| F3 | Widget und fremde Herkünfte waren im Modus `phonebook` möglich (anonymer Zugang) | mittel | ADR-030 |
| F4 | Transkripte ohne Frist (`0` = unbegrenzt) möglich | mittel | ADR-011: 14 Tage Standard, > 365 kein Start |
| F5 | Gesperrte AD-Konten blieben im Telefonbuch bis `sessionMax` angemeldet | mittel | ADR-013 RC, `auth.adRecheck` |
| F6 | Ganzes Docker-Netz galt als vertrauenswürdiger Proxy; andere Container hätten `X-Forwarded-For` fälschen können | mittel | nur Gateway `/32`, `ip_range` |
| F7 | Die Gateway-Adresse konnte von einem anderen Container belegt werden (Start schlug fehl) | niedrig (Verfügbarkeit) | `ip_range` |
| F8 | `PHONEBOOK_INTERNAL_URL=http://app:8080` wurde abgelehnt – die Anleitung seit PH-01 startete nicht | niedrig (Verfügbarkeit) | ADR-027 |
| F9 | `.gitignore` hätte die Stack-Konfiguration und `secrets/create.sh` ausgeschlossen | niedrig | Muster nur im Wurzelordner |
| F10 | Healthchecks brauchten `wget` im Image | niedrig | `ucng healthcheck`, `ucng-voice healthcheck` |
| F11 | Ein Vorab-Tag hätte im Telefonbuch-Image `latest` gesetzt | niedrig | Bedingung im Workflow |

## 4. Nicht ausgeführt – vor dem Produktivbetrieb nachholen

| Was | Warum nicht | Wie nachholen |
|---|---|---|
| Echte Images (MySQL 8.4, Caddy 2.10, distroless, alpine) | Registries in der Testumgebung gesperrt; der Stack lief mit Platzhalter-Images aus denselben Programmen (statisch gebaut) und einem MySQL-kompatiblen Ersatz | Workflow `stack.yml` (startet mit echten Images, TLS `internal`, `check.sh`, Sicherung, Sprach-Server) |
| Caddyfiles (Gateway, Sprach-Server) | kein Caddy verfügbar; ersetzt durch den Test-Gateway `devgateway` (http) | `stack.yml` (`caddy validate` in allen TLS-Arten, dann `check.sh` mit TLS) |
| `govulncheck` | Modul-Proxy gesperrt | CI beider Repositories |
| `go.sum` vollständig | dito; die Builds nutzen `go mod tidy` | im Repository `go mod tidy` ausführen und einchecken |
| Echtes AD | nur nachgebildetes LDAP | Abnahme vor Ort |
| Firefox, Edge, Safari; echte Mikrofone/Kameras; Bildschirmfreigabe | nur Chromium mit simulierten Geräten | Abnahme |
| UCNG mit SQLite | Treiber hier nicht ladbar | CI (`TAGS="sqlite mysql"`) |

## 5. Restrisiken

Siehe [Bedrohungsmodell §4](ph02-threat-model.md#4-restrisiken-bewusst). Die wichtigsten:
- Kompromittierter UCNG-Server (liefert JavaScript aus) – E2EE schützt davor nicht.
- AD-Störung: Das Telefonbuch selbst ist fail-open.
- Webhooks dürfen private Netze erreichen.
- Aufnahmen liegen auf Endgeräten.
- **Lizenzprüfung (ADR-016) offen.**
