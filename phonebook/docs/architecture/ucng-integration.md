# UCNG als Kommunikations-Add-on – Bestandsaufnahme (PH-00)

**Stand:** 26.10.2026 · **Phasen:** PH-00 (Bestandsaufnahme, §1–4) und PH-01 (Schalter, Routing, Gate A, §5) · **Grundlage:** `PHONEBOOK_UCNG_INTEGRATIONSFAHRPLAN.md`

§1–4 halten den Ist-Zustand beider Codebasen aus PH-00 fest (unverändert gelassen, damit der Ausgangspunkt nachvollziehbar bleibt). §5 beschreibt, was PH-01 umgesetzt und geprüft hat.

Begleitdokumente:
- [`docs/ucng-feature-parity.csv`](../ucng-feature-parity.csv) – maschinenlesbare Funktionsinventur (79 Einträge), geprüft durch `docs_parity_test.go`
- [`docs/integration-decisions.md`](../integration-decisions.md) – Architekturentscheidungen (ADR) und offene Entscheidungen

---

## 1. Ausgangsstand

| | Phonebook | UCNG |
|---|---|---|
| Quelle | Stand `telefonbuch (18).zip` (= Arbeitsstand vor PH-00) | `ucng-main (1).zip`, SHA-256 `d936c641179900b75276f648ed9b3e0625385806bab154688a741d5f3e60fe5c` |
| Baseline-Commit (lokal) | `026aee7` „PH-00 Baseline: Stand telefonbuch (18).zip“ | `7797b5a` „PH-00 Baseline: ucng-main (1).zip unverändert importiert“ |
| Sprache/Toolchain | `go 1.24` (go.mod), getestet mit go1.24.7 | `go 1.27`, `toolchain go1.27.2`; getestet mit go1.27.2 (aus den offiziellen Quellen gebaut, Tag `go1.27.2`) |
| Module | go-ldap v3.4.11, go-sql-driver/mysql v1.9.3 | Standard-Build **ohne** Fremdmodule; mysql v1.10.1 nur mit `-tags mysql`, modernc.org/sqlite v1.60.1 nur mit `-tags sqlite` |
| Umfang | ca. 70 Dateien | 152 Go-Dateien (ca. 37 500 Zeilen im Hauptpaket), 53 Testdateien, 18 JS-Dateien |
| Lizenz | intern | AGPL-3.0-or-later, `NOTICE`, `TRADEMARKS.md`, CLA |

Die lokalen Commits sind Baselines in dieser Arbeitsumgebung. In die echten Repositories (Phonebook: GitHub `b1tsnetwork/phonebook`) kommt nur der PH-00-Änderungssatz als Patch/PR.

## 2. Testresultate (tatsächlich ausgeführt)

### Phonebook (go1.24.7, linux/amd64)

| Prüfung | Ergebnis |
|---|---|
| `gofmt -l .` | 0 Abweichungen |
| `go vet ./...` | ohne Befund |
| `go test -race -count=1 ./...` mit MySQL (`TB_TEST_DSN`, go-mysql-server) | **31 bestanden, 0 fehlgeschlagen, 0 übersprungen** |
| `node --check web/js/*.js` | alle fehlerfrei |
| PowerShell-Parser (`Telefonbuch-Tray.ps1`, `Install-Telefonbuch.ps1`) | 0 Parserfehler |

Hinweis zur Reproduzierbarkeit: Diese Umgebung hat keinen Zugang zu `proxy.golang.org`. Die Phonebook-Module kamen aus dem lokalen Modul-Cache bzw. per `replace` von den GitHub-Spiegeln. In der GitHub-CI (`.github/workflows/docker.yml`) läuft derselbe Testlauf mit MySQL 8.4 und dem normalen Modulproxy.

### UCNG (go1.27.2, Standard-Build = Dateispeicher)

| Prüfung | Ergebnis |
|---|---|
| `gofmt -l .` | 0 Abweichungen |
| `go vet ./...` | ohne Befund |
| `go test -race -count=1 ./...` | **223 bestanden, 0 fehlgeschlagen, 4 übersprungen** (Pakete: `ucng`, `cmd/ucng-beacon`, `cmd/ucng-voice`, `examples/bot-music`, `internal/*`) – Laufzeit ca. 1 min 45 s |
| übersprungen | `TestSQLRoundTrip`, `TestSQLImportsFiles`, `TestSQLBackupExport` (brauchen `-tags sqlite`); `TestQRExport` (manuelles Hilfswerkzeug, nur mit `QR_OUT`) |
| `go build -tags mysql` + `go vet -tags mysql` | erfolgreich |
| `-tags sqlite` | **nicht prüfbar**: `modernc.org/sqlite` liegt auf GitLab, das von hier gesperrt ist; die GitHub-Spiegel haben die Version v1.60.1 nicht |
| `node --check` (18 Dateien aus `web/`, `web/embed/`, `cmd/ucng-beacon/web/`) | alle fehlerfrei |
| Smoke-Test der Binärdatei | `/healthz` 200, `/` 200, `/admin` 200 mit `X-Frame-Options: DENY`, `/api/rooms` 200, `/.well-known/ucng` 404 (Modus simple ohne `PUBLIC_URL`), `POST /api/session` vergibt ein Namens-Token ohne Konto |

Bekannte Altfehler: keine. Die einzige Lücke ist die SQLite-Variante, und sie liegt an der Umgebung, nicht am Code. UCNGs eigene CI (`.github/workflows/ci.yml`) baut alle vier Varianten (`""`, `sqlite`, `mysql`, `sqlite mysql`) und muss für SQLite maßgeblich bleiben.

## 3. Phonebook: Schnittstellen für die Integration

### 3.1 Anmeldung und Sitzung (`auth.go`)
- `Session` (auth.go:80): `IDHash`, `Username`, `DisplayName`, `IsAdmin`, `Source` (`ldap` | `local` | `dev`), `CSRF`, `LastSeen`, `ExpiresAt`, `EmployeeID` (0 = kein Telefonbucheintrag).
- Cookie `__Host-tb_session` (HTTPS) bzw. `tb_session`, `HttpOnly`, `SameSite=Strict` (auth.go:101, 183). Ändernde Anfragen brauchen den `X-CSRF-Token` und eine passende Origin (`requireAuth`, auth.go:241).
- `IsAdmin` ist eine **Momentaufnahme beim Login**: LDAP-Admin-Gruppe (`ldap.adminGroupDN`, ldap.go:230) oder lokales Konto. Während der Sitzung prüft niemand neu. Wer im AD gesperrt wird, behält seine Sitzung bis `sessionIdle`/`sessionMax`.
- Zugangsbeschränkung: optional `ldap.userGroupDN` (nur Mitglieder dürfen sich anmelden, `errNotAllowed`).
- Gastzugang: `auth.publicDirectory` – das Telefonbuch ist dann ohne Anmeldung lesbar (`optionalAuth`, auth.go:269). **Nach Fahrplan keine Eintrittskarte zur Kommunikation.**
- Abmelden: `destroySession` (auth.go:216). Es gibt noch keinen Haken für nachgelagerte Systeme (Widerruf von UCNG-Sitzungen).

### 3.2 Identität
- Es gibt **keine stabile `principal_id`**. Eine Person ergibt sich aus `Username` + `Source`.
- LDAP liefert beim Login `LDAPUser.GUID` (objectGUID, ldap.go:100). Die GUID landet aber nicht in der Sitzung, sondern nur über den AD-Abgleich in `EmployeeRec.ADGUID` (store.go:175).
- `EmployeeRec.Username` verknüpft einen Eintrag mit einem Konto. `EmployeeID` kann 0 sein (lokales Konto, Dienstkonto) – nach Fahrplan kein Ausschlussgrund.
- Private Kontakte, Kontaktwege und Einrichtungen (Block-Avatare) sind **keine** anmeldbaren Personen.

### 3.3 Organisation und Rechte
- `OrgUnit` (store.go:205) mit Eltern-Verweis, Ebenen aus dem Setting `orgLevels`, Leitung/Stellvertretung per Schlagwort oder fest eingetragen. `EmployeeRec.OrgUnit` (store.go:184) ist die **tatsächliche** Zuordnung. Der Amt-Freitext ist nur eine Anzeige.
- Rechte im Phonebook: nur `IsAdmin` bzw. die eigene Person. Ein Rechtemodell wie `communication.use` gibt es noch nicht.
- `channels.go` verwaltet **Kontaktwege** (Ticket/Mail/Hotline), keine Chat-Kanäle. Neue Typen brauchen eigene Namen (z. B. `CommunicationRoomMapping`).

### 3.4 Betrieb
- Health/Monitoring: `/livez`, `/readyz` (Details nur für interne Netze bzw. mit Token), `/healthz`, `/metrics` (Prometheus) – siehe README *Überwachung*.
- Datenhaltung MySQL oder JSON, Rückfall auf die JSON-Kopie (nur lesend). Die Reserve sichert **nur** Phonebook-Daten und darf UCNG-Daten weder enthalten noch überschreiben.
- CSP: `default-src 'self'`, `frame-ancestors 'none'`, `frame-src` = `default-src` (also nur `self`). Ein iframe zu einer anderen Origin wäre heute blockiert.

### 3.5 Befund außerhalb des Fahrplans
- *(in PH-01 behoben, ADR-012)* `sameOrigin` (auth.go:309) vergleicht die Origin mit `publicURL` per **Präfix**. `https://tb.firma.local` würde auch `https://tb.firma.local.angreifer.example` durchlassen. Die Wirkung ist gering, weil zusätzlich das CSRF-Token nötig ist. Der Fahrplan nennt die Korrektur ausdrücklich („Origin-Prüfung exakt machen“); sie ist für PH-01 eingeplant (ADR-012).

## 4. UCNG: Inventur

### 4.1 Programme und Prozesse
| Programm | Zweck | Konfiguration |
|---|---|---|
| `ucng` (Hauptpaket) | Chat, Admin, Voice-Steuerung, eingebauter Voice-Relay, Föderation | `deploy/chat/.env.example` |
| `cmd/ucng-voice` | zusätzlicher Sprach-Server (Agent) | `deploy/voice/.env.example`, `docs/voice-deployment.md` |
| `cmd/ucng-beacon` | Instanz-Verzeichnis, GIF-Hub | `deploy/beacon/.env.example` |
| STT (speaches, extern) | OpenAI-kompatible Spracherkennung | `deploy/stt/.env.example` |
| `examples/bot-music` | Musik-Bot | `deploy/music/.env.example` |

Dockerfiles: `Dockerfile`, `Dockerfile.voice`, `Dockerfile.beacon`, `Dockerfile.music`.

### 4.2 Betriebsarten (`mode.go`)
- `simple` (Standard): Namen ohne Konto, Token im Browser. Gäste dürfen **immer** schreiben (`guestPolicy()` liefert außerhalb von `hosted` fest `GuestsWrite`, account_api.go:41).
- `hosted`: Konten (Passwort, TOTP, E-Mail), Gäste-Regel einstellbar (none/read/write), E2EE-DMs, Föderation.
- **Folgerung:** Ein künftiger Modus `phonebook` muss auf dem Konto-Modell von `hosted` aufsetzen. Gäste sind dabei fest `none`, Registrierung und Passwort-Login sind geschlossen. Ein Aufbau auf `simple` wäre unsicher (Gäste schreiben immer).

### 4.3 Identität und Sitzungen – zwei getrennte Systeme
1. **Chat-Nutzer** (`users.go`, `members.go`): `user{ID (öffentlich, stabil), Name, TokenHash, Roles, Team, Member *member}`.
   - Jede API-Anfrage löst die Person über **eine** Stelle auf: `Users.Lookup(bearer(r))` (users.go:267, api.go:98).
   - Konto-Sitzungen entstehen in `Users.newSessionLocked` (members.go:470) und lassen sich über `endSessionsLocked` beenden.
   - `OnSessionsEnded` → `hub.DropViewer` schließt offene Streams (main.go:312).
   - Höchstens 20 Sitzungen je Konto (`maxMemberSess`, members.go:36).
2. **Admin-Bereich** (`accounts.go`): eigene Konten mit `Role` = admin | moderator, Superadmin über `ADMIN_PASSWORD`. Die Middleware `requireRole` prüft `Accounts.Session(bearer)` (accounts.go:390).
   - Zwischen Chat-Person und Admin-Konto gibt es **keine** Verbindung.

**Token-Transport:**
- Chat: `Authorization: Bearer`, Token in `localStorage` (web/app.js:12, 113).
- SSE (`EventSource` kann keine Header): Query-Parameter `?viewer=<id>.<HMAC>.<sessionID>`. Die Signatur ist an die Konto-Sitzung gebunden (`viewerOf`, api.go:501; `mayRead`, account_api.go:49).
- Voice: kurzlebige, signierte Tickets (`voicerelay.SignTicket`, voice.go:1078), Rate-Limit 30/min je IP.

**Andockstellen für PH-02:**
- eine Schattenkonto-Sitzung über `newSessionLocked` ausstellen; alle Chat-, SSE- und Voice-Pfade akzeptieren sie, ohne dass sie geändert werden müssen;
- Widerruf über `endSessionsLocked` + `DropViewer`;
- für Admin-/Moderatorrechte zusätzlich eine `Accounts`-Sitzung ausstellen oder die Middleware um eine Rollenquelle erweitern (offene Entscheidung, ADR-008).

### 4.4 Rechte (`roles.go`, `access.go`)
- Rollen: frei definierbar, höchstens 30, IDs `r_[a-z0-9]{4,16}` (unveränderlich, Name änderbar).
- Rechte (`Perms`): `links`, `noSlow`, `protected`, `pin`, `upload`, `delete`, `mute`, `tempRooms`. Das Team-Abzeichen hat alle.
- Kanäle: `readRoles` (privat), `writeRoles` (nur bestimmte schreiben), DMs nur für die Beteiligten. Die Prüfung läuft **serverseitig** (`room.canRead`/`canWrite`, `Hub.CanRead`).
- Für Voice gibt es Kanal- und Gäste-Regeln. Ein Rollenrecht `voice.join`/`voice.speak` existiert nicht. Die Rechtenamen des Fahrplans (`read`, `write`, `voice.join`, `voice.speak`, `moderate`, `manage.rooms`, `transcript.read`, `communication.admin`) sind Neuentwurf.
- Kanalgruppen (`layout.go`) sind nur Darstellung.

### 4.5 E2EE (`e2e.go`, `web/account.js`)
- ECDH P-256 je Konto, AES-GCM je Unterhaltung, nur in DMs zwischen Konten (`hosted`), nur Text.
- Der private Schlüssel liegt **verschlüsselt auf dem Server**. Den Schlüssel dazu leitet allein der Browser aus dem **UCNG-Passwort** ab.
- Sicherheitsnummer (60 Ziffern) und Erkennung von Schlüsselwechseln (TOFU) laufen im Browser.
- Mit Phonebook-SSO fehlt das Passwort und damit die Schlüsselableitung → Kernproblem von PH-02 (ADR-007).

### 4.6 Medien
- Uploads mit Bildoptimierung (`imgscale`), Emotes, GIFs (eigene Bibliothek, GIPHY/Tenor, Beacon), Profilbilder.
- Private Kanäle/DMs: Dateien mit Präfix `p_`, abrufbar nur mit signierter Adresse `?exp=&sig=`. Diese ist **1–2 Tage gültig und nicht an eine Person gebunden** (uploads.go:308 ff.). Nach einem Rechteentzug bleiben schon ausgelieferte Links bis zum Ablauf nutzbar (ADR-010).
- Keine allgemeine Dateiablage. Keine Medien in E2EE-DMs.

### 4.7 Voice, Konferenz, Transkription
- Opus über WebSocket; eingebauter Relay und/oder Agenten (`VOICE_AGENT_TOKEN`); Bildschirm und Kameras können ausgelagert werden (`VOICE_OFFLOAD_SCREEN/VIDEO`).
- Bühne, Handzeichen, Leitung/Co-Hosts, Personen verschieben, Breakouts, Aufnahme im Browser (MediaRecorder/WebM), E2EE-Voice (schließt Untertitel und Musik aus).
- STT: `STT_URL` (leer = aus), `TRANSCRIPT_KEEP_DAYS` mit **Standard 0 = unbegrenzt** (main.go:148).
- CSP `connect-src` enthält die Voice-Server (`voiceConnectSrc`). `Permissions-Policy` erlaubt Kamera/Mikrofon nur für `self`.

### 4.8 Föderation und Beacon
- DM-Föderation: nur mit `hosted` + `PUBLIC_URL` + Admin-Schalter `Fed.Enabled` (federation.go:261).
- Kanal-Föderation: `PUBLIC_URL` + Admin-Schalter, **auch im Modus simple**.
- Beide Schalter liegen nur im Admin-Bereich (Laufzeit). Für das Phonebook-Profil fehlt eine Sperre auf Betriebsebene, die Admins nicht umgehen können (ADR-009).
- Die Föderation lehnt Ziele im lokalen Netz ab (Schutz vor SSRF), außer mit `FED_INSECURE`.

### 4.9 Routen
277 registrierte Muster: 228 im Hauptpaket, 35 in `cmd/*`, 13 in `internal/*`, 1 in `examples/`.

| Bereich | Anzahl | Schutz |
|---|---|---|
| `/api/admin/*` | 113 | `requireMod`/`requireAdmin` (Admin-Konten) |
| `/api/account/*` | 20 | `hostedOnly` |
| `/api/bot/*` | 19 | `requireBot` (Bot-Token) |
| `/api/rooms/*` | 18 | Bearer + `readGate` + Kanal-ACL |
| `/api/voice/*` | 12 | Bearer bzw. Agent-Token (`/api/voice/agents/*`) |
| `/api/fed/*`, `/.well-known/ucng`, `/.well-known/gochat`, Inbox | 8 | Föderations-Signaturen |
| `/api/channels/remote/*` | 4 | Bearer |
| `/api/dms*`, `/api/blocks`, `/api/profile/*`, `/api/profiles`, `/api/session`, `/api/unread` | 15 | Bearer |
| `POST /api/users/{id}/mute`, `GET /api/transcripts/{id}` | 2 | Bearer + Rechteprüfung |
| öffentlich: `/api/settings`, `/theme.css`, `/api/hours`, `/api/media`, `/api/roles`, `/api/groups`, `/api/commands`, `/api/gifs*`, `/media/{kind}/{file}`, `/api/widgets/{id}`, `/healthz` | 14 | keine bzw. Signatur bei privaten Medien |
| Seiten: `/`, `/admin`, `/admin.html` | 3 | – |

Echtzeit-Pfade: `GET /api/rooms/{room}/events` (SSE), `GET /api/admin/rooms/{room}/events` (SSE), `GET /api/bot/events` (SSE), `GET /api/voice/ws` und die Relay-WebSockets (`wss://`).

### 4.10 Frontend und Pfade
- `web/` ist eingebettet (`internal/webstatic`, versionierte Adressen, gzip, ETag).
- **Absolute Pfade:** `/api` kommt in `admin.js` (125×), `app.js` (37×), `account.js` (22×), `embed/chat.js` (16×), `voice*.js` (17×) vor; in `index.html` und `admin.html` stehen 27 `src="/…"`/`href="/…"`. WebSocket-Adressen werden aus `location.host` gebildet (voice.js:191).
- Es gibt keine Basis-Pfad-Variable. Ein Betrieb unter `/communication/` braucht einen systematischen Umbau von Frontend, Server-Routen, CSP und `.well-known` (ADR-004).
- Framing: Nur `/admin` sendet `X-Frame-Options: DENY`. Die Chat-Seite hat weder `X-Frame-Options` noch `frame-ancestors` und lässt sich daher von jeder Seite einbetten (Clickjacking-Prüfung in PH-11). Eine Einbettung in das Phonebook bräuchte dort `frame-src` und eine Freigabe für Kamera/Mikrofon über `allow`.

### 4.11 Konfiguration (Auszug, Chat-Server)
`ADDR`, `ADMIN_PASSWORD`, `PUBLIC_URL`, `MODE`, `DATA_DIR`, `STORAGE`, `DB_DSN`, `BACKUP_DIR`, `BACKUP_KEEP`, `ALLOWED_ORIGINS`, `TRUST_PROXY`, `TRUSTED_PROXIES`, `RATE_LIMIT`, `BOT_RATE`, `CLAIM_RATE`, `MAX_USERS`, `NAME_TTL_DAYS`, `MAX_ROOMS`, `MAX_STREAMS`, `MAX_STREAMS_PER_IP`, `SOURCE_URL`, `VOICE`, `VOICE_BUILTIN`, `VOICE_MAX_PARTICIPANTS`, `VOICE_MAX_MBIT`, `VOICE_MAX_PER_IP`, `VOICE_AGENT_TOKEN`, `VOICE_OFFLOAD_SCREEN`, `VOICE_OFFLOAD_VIDEO`, `STT_URL`, `STT_MODEL`, `STT_TOKEN`, `STT_LANGUAGE`, `STT_CONCURRENCY`, `TRANSCRIPT_KEEP_DAYS`, `FED_INSECURE`. Vollständig mit Standardwerten: UCNG-README, Abschnitt *Konfiguration*.

### 4.12 Überschneidungen mit dem Phonebook
- **Öffnungszeiten** (`/api/hours`, Admin-Einstellung) ↔ Phonebook-**Sprechzeiten** (je Einheit/Aufgabe).
- **Profilbild** ↔ Phonebook-**Pixel-Avatar**.
- **Melden** (UCNG: Nachrichten) ↔ Phonebook-**Meldungen** (Stammdaten). Fachlich getrennt, nur Begriffe abgrenzen.
- **Health/Monitoring:** UCNG `/healthz` liefert immer `ok` und prüft nichts; das Phonebook hat `/readyz` und `/metrics`.

## 5. PH-01 – Umsetzung und Ergebnis (Gate A)

**Entscheidung des Projektinhabers (26.10.2026):** Subdomain-Variante (ADR-004).

### 5.1 Phonebook
- **Konfiguration** `communication` (`communication.go`), Standard `enabled=false`. Ist das Add-on an, wird streng geprüft:
  - `publicBaseURL`: https, nur Wurzelpfad (Subdomain), **andere Herkunft** als `publicURL` (normierter Vergleich inkl. Standard-Port); http nur für `localhost`/`*.localhost`;
  - `internalBaseURL`: http(s), ohne Zugangsdaten, Abfrage und Fragment;
  - `serviceCredentialFile`: muss existieren, normale Datei, nicht für alle lesbar, Inhalt ≥ 32 Zeichen;
  - `defaultTranscriptRetentionDays`: 1–365 (0 = unbegrenzt ist verboten, ADR-011);
  - `probeInterval`: 5 s bis 1 h;
  - Funktionsschalter mit Standardwerten: Chat, Medien, Voice, temporäre Räume und Bots an; Transkription, Föderation und Beacon aus.
  - Umgebungsvariablen: `TB_COMMUNICATION_ENABLED`, `…_PUBLIC_URL`, `…_INTERNAL_URL`, `…_SECRET_FILE`.
- **Add-on aus:** keine Add-on-Routen (404), kein Hintergrund-Job, keine Verbindung, nichts in `/readyz` oder `/metrics` (Test `TestCommunicationDisabledIsInvisible`).
- **Prüfung im Hintergrund:** `GET {internal}/healthz` und `/api/settings`. Weiterleitungen werden nicht verfolgt, Antworten auf 256 KB begrenzt, Timeout 4 s.
  - Das **Sicherheitsprofil** wird gegen die gemeldeten Werte geprüft: `appMode=hosted`, `guests=none`, keine offene Registrierung, keine DM- oder Kanal-Föderation (sofern nicht freigegeben), Voice wie konfiguriert.
  - Ergebnis: `ok`, `insecure`, `unreachable` oder `pending`. Nur `ok` gibt die Kommunikation frei (Fahrplan-Kriterium 4: überprüfbare Schalter).
- **Endpunkte und Metriken:**
  - `GET /api/addons/communication/status`: nur angemeldet, Gäste erhalten 401; Admins sehen Prüfdetails und die interne Adresse.
  - `/readyz` mit Check `communication`: höchstens `degraded`, nie `down`.
  - Metriken `tb_communication_up`, `tb_communication_secure`, `tb_communication_probe_seconds`.
- **ADR-012 umgesetzt:** `sameOrigin` vergleicht exakt Schema, Host und Port (`normOrigin`). Präfix-, Port-, Schema-, `null`- und Userinfo-Varianten werden abgewiesen (Test `TestSameOriginExact`).
- **Prüfwerkzeug** `telefonbuch commcheck`: Routing-Smoke-Test über den echten Gateway (siehe 5.3).
- **Bereitstellung:**
  - `compose.communication.yml` als Zusatzdatei, Profile `communication`, `voice` und `stt`. Die Basisdatei bleibt unverändert.
  - Das Sicherheitsprofil steht fest in der Compose-Datei; `UCNG_IMAGE` ist Pflicht.
  - Vorlagen: `deploy/communication/` (Anleitung, Env-Vorlagen) und `deploy/gateway/` (nginx, Caddy).
  - `tools/devgateway`: Host-basierter Test-Gateway. Mit `-buffer` bzw. `-strip-upgrade` stellt er Proxy-Fehler nach.

### 5.2 UCNG (Branch `phonebook-integration`, ADR-017)
- **Neu:** `oplocks.go` mit Betriebssperren `LOCK_GUESTS`, `LOCK_REGISTRATION`, `LOCK_FEDERATION` und `LOCK_CHANNEL_FEDERATION` (Flags `-lock-*`).
  - Sie werden in `Hosted.Config()` und `ChanFed.Config()` angewendet und gehen allen Admin-Einstellungen vor.
  - Admin-Änderungen dagegen → **409** statt stillem Überschreiben.
  - Im Admin-Bereich: Felder gesperrt, Zeile „Betriebssperren“.
- **Start-Prüfung:** Sperren mit `MODE≠hosted` bzw. ein unbekannter `MODE` beenden den Start. Ohne Sperren fällt ein unbekannter Modus wie bisher auf `simple` zurück.
- **Andockpunkte im Bestand** (je eine markierte Zeile/Block): `hosted.go`, `chanfed.go`, `main.go`, `account_api.go`, `web/admin.js`, i18n (de/en, inkl. Beacon-Kopie). Dazu README, `deploy/chat/.env.example` und `deploy/chat/compose.yml` (Durchreichen der Variablen).
- **Bewusst noch nicht gesperrt:**
  - Admin-Einladungen (Testkonten bis PH-02);
  - Passwort-Login für eingeladene Konten;
  - Beacon und externe GIF-Anbieter.

  Diese Punkte folgen mit dem Modus `phonebook` (ADR-005, PH-02) bzw. in PH-10.

### 5.3 Routing-Smoke-Test (tatsächlich ausgeführt)
Aufbau:
- `tools/devgateway` auf `127.0.0.1:8180` mit Host-Routing `telefonbuch.localhost` → Phonebook, `kommunikation.localhost` → UCNG (Branch-Build, alle Sperren gesetzt);
- Phonebook mit `communication.enabled=true`;
- `telefonbuch commcheck … -connect-to 127.0.0.1:8180 -ucng-admin-password-file …`.

| Lauf | Ergebnis |
|---|---|
| Sollkonfiguration | **15/15 bestanden**: getrennte Herkunft, `/livez`, Add-on aktiv (401 ohne Anmeldung), UCNG-Startseite mit CSP, `/healthz`, Sicherheitsprofil, `/.well-known/ucng` ohne Föderation, 6× kein Gastzugang (Kanäle, Verlauf, SSE, Profile, Gastname, Registrierung), WebSocket `/api/voice/ws` (101 → Nachricht → Schließcode 4400), SSE erste Daten nach 1 ms |
| Gateway puffert (`-buffer`) | erkannt: „SSE ohne Proxy-Puffer – keine Antwort binnen 3s“ |
| Gateway ohne Upgrade (`-strip-upgrade`) | erkannt: „kein Upgrade: HTTP 400“ |
| UCNG-Standard ohne Sperren (`MODE=simple`) | erkannt: Sicherheitsprofil + 5× Gastzugang. Das Phonebook meldet `insecure`, `/readyz` meldet `degraded` (200) |
| `MODE=simple` + `LOCK_GUESTS` / `MODE=hostd` + Sperre | UCNG verweigert den Start mit klarer Meldung |
| Statusendpunkt über Gateway | Person: `available:true`, ohne interne Details; Admin: mit Prüfdetails; Gast: 401 |
| Präfix-Angriff auf die Origin (`…:8180.evil`) | abgewiesen („Ungültige Herkunft der Anfrage“) |

Nicht in dieser Umgebung ausgeführt:
- echte nginx/Caddy-Instanz (Vorlagen in `deploy/gateway/`, nicht installierbar);
- Docker-Daemon. Die Compose-Dateien sind mit `docker compose config` geprüft: Basis allein ohne Secret und ohne UCNG; mit Zusatzdatei und Profil zusätzlich `ucng`; ohne `UCNG_IMAGE` eine klare Fehlermeldung.

**Vor dem Pilotbetrieb** `telefonbuch commcheck` gegen den echten Gateway laufen lassen.

### 5.4 Messung Pfad-Präfix (ADR-004: nur gemessen)
- UCNG-Startseite: 15 absolute Verweise auf Skripte, Stile und Bilder.
- Frontend-Skripte: 227 absolute Pfade (`admin.js` 130, `app.js` 40, `account.js` 22, `embed/chat.js` 18, `voice*.js` 17).
- 3 Stellen bilden WebSocket-Adressen aus `location.host`.
- `/.well-known/ucng` muss an der Wurzel liegen.
- **Gespeicherte Nachrichten enthalten Bildadressen `/media/uploads/…`.** Ein Präfix-Betrieb bräuchte also neben dem Umbau eine Datenmigration.

Die Subdomain-Entscheidung ist damit bestätigt.

### 5.5 Gate A
| Kriterium | Stand |
|---|---|
| Phonebook startet und besteht alle Tests **ohne** UCNG | erfüllt (Add-on standardmäßig aus; eigener Test) |
| Mit Add-on sind die UCNG-Endpunkte gesichert erreichbar | erfüllt (commcheck 15/15 über Gateway) |
| Kein ungeschützter Gastzugang zum integrierten Chat | erfüllt, sofern das Image mit Betriebssperren läuft – ein Image ohne Sperren wird als `insecure` erkannt und nicht freigegeben |
| Offen bis zur Freigabe | Lauf von `commcheck` gegen den echten Gateway; Merge des UCNG-Branches bzw. eigenes Image |

## 6. PH-02 – Identität und gemeinsame Anmeldung (Gate B)

Entscheidungen des Projektinhabers (26.10.2026): **ADR-007 A** (eigenes Entsperr-Geheimnis + Wiederherstellungscode) und **ADR-008** (ausdrückliche Zuordnung, Telefonbuch-Admins nur zur Inbetriebnahme automatisch). Bedrohungsmodell: [`../security/ph02-threat-model.md`](../security/ph02-threat-model.md).

### 6.1 Ablauf

```
Browser (UCNG-Tab)                Telefonbuch                         UCNG
 state, verifier (sessionStorage)
 ──GET /api/addons/communication/authorize?state&challenge=S256──►
                                  Sitzung? sonst Anmeldung, dann weiter
                                  Nutzungsrecht, UCNG-Profil ok?
 ◄──303 <UCNG>/#pb_code=…&pb_state=…  (Code 60 s, einmalig, nur Hash gespeichert)
 state prüfen, Fragment entfernen
 ──POST /api/integration/phonebook/exchange {code, verifier}──────────────────────►
                                  ◄──POST /api/internal/communication/redeem (HMAC-Schlüssel)
                                  ──Identität: principal_id, Name, Rolle, Sitzungsbezug──►
                                                                        Schattenkonto, Sitzung
 ◄───────────────────────────────────────────────────────────────── Sitzung (Bearer)
```

Danach: UCNG prüft alle 5 min (`/api/internal/communication/validate`), das Telefonbuch meldet Abmeldung/Sperre sofort (`/api/integration/phonebook/revoke`), der Verzeichnisstatus wird höchstens alle 15 min nachgeprüft (ADR-013).

### 6.2 Phonebook
| Datei | Inhalt |
|---|---|
| `migrations/010_principals.sql`, `store_principals.go` | `principals` (MySQL + JSON, Export/Import), `sessions.principal_id` |
| `commsso.go` | Konfiguration `access`/`roles`/`adRecheck`/`adGrace`, `authorize`, `redeem`, `validate`, Codes im Speicher, AD-Nachprüfung, Widerruf-Warteschlange, `/api/me` → `communication` |
| `auth.go` | Principal bei der Anmeldung, Widerruf beim Abmelden |
| `ldap.go` | Gruppen-Momentaufnahme (`extraGroups`), `LookupUser` (deaktiviert, gelöscht, `userGroupDN`) |
| `communication.go`, `commcheck.go` | UCNG muss `MODE=phonebook` mit dieser Telefonbuch-Adresse melden |
| `web/js/app.js` | Menüpunkt „💬 Kommunikation“, Weiterleitung nach der Anmeldung |

### 6.3 UCNG (Branch `phonebook-integration`)
| Datei | Inhalt |
|---|---|
| `phonebook.go` | `MODE=phonebook`, `PHONEBOOK_*`, Exchange, Revoke, Admin-SSO, Nachprüfungsschleife, DM nur verschlüsselt |
| `phonebook_users.go` | Schattenkonten (`member.ext`, Index `byExt`), Sitzungsfristen (`PB`, `Until`, `Checked`), fail-closed |
| `phonebook_e2ee.go` | Entsperr-Geheimnis, Wiederherstellungscode, Wechsel, Zurücksetzen |
| `oplocks.go`, `main.go`, `members.go`, `users.go`, `accounts.go`, `voice.go`, `api.go`, `uploads.go` | je ein markierter Andockpunkt |
| `web/phonebook.js`, `web/account.js`, `web/app.js`, `web/admin.js`, CSS, i18n (de/en) | Anmeldeablauf, E2EE-Dialoge, Admin-Anmeldung |

### 6.4 Testresultate (tatsächlich ausgeführt)
Siehe PR-Bericht `PH-02-PR.md`. Kurz: Go-Tests beider Projekte mit Race-Detector grün (inkl. MySQL), Browser-Ende-zu-Ende mit echten Prozessen (Telefonbuch, UCNG `MODE=phonebook`, Gateway, Test-AD) grün, `commcheck` 16/16.

### 6.5 Gate B
| Kriterium | Stand |
|---|---|
| Keine zweite Anmeldung, kein Passwort in UCNG | ✓ Endpunkte geschlossen, alte Sitzungen ungültig |
| Stabile Identität, keine Verknüpfung über Namen | ✓ objectGUID / lokale ID |
| Code: kurzlebig, einmalig, gebunden, kein Token in URLs an Server | ✓ PKCE + state, Fragment |
| Widerruf bei Abmeldung/Sperre inkl. Streams und Sprache | ✓ |
| Ausfall Telefonbuch/AD: fail-closed, ohne Datenverlust | ✓ |
| E2EE nicht geschwächt, Mehrgeräte + Wiederherstellung | ✓ (DMs nur verschlüsselt) |
| Admin-/Moderatorrechte nur durch Zuordnung | ✓ |
| Abnahme durch Projektinhaber | ADR-013 bestätigt (26.10.2026); offen: Test mit echtem AD |

## 7. PH-03 – Organisation und Rechte

Entscheidungen des Projektinhabers (26.10.2026): ADR-013 angenommen; **ADR-010 (a)** kurze, sitzungsgebundene Bildadressen; **ADR-021** Kanal einer Einheit = Einheit + Untereinheiten, Leitung/Vertretung moderiert; **ADR-022** Austritt: sperren, nach 90 Tagen anonymisieren.

### 7.1 Datenfluss
```
Telefonbuch                                              UCNG
 Organisation (jede Minute geprüft, Hash) ──PUT /api/integration/phonebook/org──► Gruppenkatalog pb:org/pb:lead,
                                                                                 Kanäle anlegen/aktualisieren/archivieren
 Identität (Einlösen, Nachprüfung 5 min) ── groups: org:<id>…, lead:<id> ──────► Zugriffsrollen der Person,
                                                                                 Live-Verbindungen nachziehen
 ◄── POST /api/internal/communication/principals (alle 6 h) ── Status aller verknüpften Personen → Austritt
```

### 7.2 Phonebook
| Datei | Inhalt |
|---|---|
| `commorg.go` | Gruppen je Person (Einheit + Oberheiten, Leitung fest oder per Schlagwort), Organisationsstand und Abgleich, Status-Endpunkt |
| `commadmin.go` | wirksame Einstellungen (Datei bzw. Verwaltung), `GET/PUT/DELETE /api/admin/communication`, `POST …/sync` |
| `commsso.go` | Gruppen in den Identitätsdaten, Einstellungen aus der Verwaltung |
| `web/js/app.js`, `web/css/app.css` | Dialog „Kommunikation verwalten“ |

### 7.3 UCNG
| Datei | Inhalt |
|---|---|
| `phonebook_org.go` | Gruppenkatalog (`phonebook-org.json`), `filterRoles`, Organisationskanäle, Moderation je Kanal (`modRoles`), `PUT …/org`, `GET /api/admin/phonebook/groups` |
| `phonebook_lifecycle.go` | Status-Abgleich, Sperre, Anonymisierung (`PHONEBOOK_LIFECYCLE`, `PHONEBOOK_RETENTION_DAYS`) |
| `uploads.go`, `mediaapi.go` | ADR-010: kanal- und sitzungsgebundene Bildadressen, Neu-Signieren; Upload-Regel „known“ für Konten aus dem Telefonbuch |
| `hub.go`, `access.go`, `chatmod.go`, `users.go`, `api.go`, `main.go` | markierte Andockpunkte (Kanal-Metadaten, Gruppen als Rollen, Rechte je Kanal, Viewer behält die Sitzung) |
| `web/app.js`, `web/admin.js`, CSS, i18n | Bild-Adressen mit Sitzung, Moderation je Kanal, Einheiten im Kanal-Editor, gesperrte Rechte bei Organisationskanälen |

### 7.4 Ergebnis
Siehe `PH-03-PR.md`: Go-Tests beider Projekte grün (inkl. MySQL, Race-Detector), Browser-Ende-zu-Ende 18/18, Mutationstests für alle Sicherungen.

## 8. PH-04: Einstieg und Navigation

### 8.1 Ablauf
- Telefonbuch: Knopf „💬 Kommunikation“ in der Kopfleiste → Kommunikation in eigenem Tab (Anmeldung wie in §6 ohne Klick).
- Kommunikation: „‹ Telefonbuch“ in der Seitenleiste, „Im Telefonbuch“ auf Profilkarten (`?q=<Name>&from=chat`), jeweils neuer Tab mit `noopener` (ADR-024).
- Keine Einbettung: `frame-ancestors 'self'` (ADR-019), geprüft von `commcheck`.
- Design-Adapter: Werkseinstellung → Titel „Kommunikation“, Akzent `#1f6f8b` (einmalig, ADR-024).
- Sprechzeiten (Telefonbuch) und Öffnungszeiten (Chat) bleiben getrennt, Hinweis im UCNG-Admin-Bereich (ADR-014).
- Telefonbuch-Ausfall: 503 statt 401, die Oberfläche behält die Sitzung (ADR-024, T8).

### 8.2 Dateien
| Projekt | Datei | Inhalt |
|---|---|---|
| Telefonbuch | `web/index.html`, `web/js/app.js`, `web/css/app.css` | Knopf in der Kopfleiste, `from=chat` |
| Telefonbuch | `commcheck.go` | Prüfung „keine Einbettung“ |
| UCNG | `phonebook_nav.go` (neu) | `frame-ancestors`, Design-Adapter, Suchbegriff fürs Telefonbuch |
| UCNG | `phonebook.go`, `phonebook_users.go`, `main.go` | 503 bei Telefonbuch-Ausfall (`pbStaleGate`, `ExtStale`), Kopfzeilen, Start |
| UCNG | `users.go`, `profile.go` | `pb` in `/api/profiles` |
| UCNG | `web/index.html`, `web/app.js`, `web/phonebook.js`, `web/admin.*`, CSS, i18n | Rücksprung, Profilkarte, Ausfall-Hinweis, Hinweis Öffnungszeiten |

### 8.3 Ergebnis
Siehe `PH-04-PR.md`.

## 9. PH-05: Chat-Funktionen im Modus phonebook

### 9.1 Prüfung der Matrix
Alle Zeilen „offen (PH-05)“ der Funktionsmatrix sind im Modus phonebook mit Konten aus dem Telefonbuch geprüft (`phonebook_chat_test.go`, Browser `ph05.js`): Echtzeit, Verlauf, Bearbeiten/Löschen, Antworten, Threads, Suche, @Name/@Rolle, Reaktionen, Anpinnen, Umfragen, Ungelesen, Melden, Präsenz, Direktnachrichten (nur verschlüsselt), Blockieren, Spam-Bremsen, Ton/Benachrichtigungen, mobil/dunkel. Lesende Endpunkte antworten ohne Sitzung mit 401.

Gefunden und angepasst: Die Link-Regel „bekannt“ (Name älter als 24 h) galt auch für neue Konten aus dem Telefonbuch. Sie gelten jetzt als bekannt, wie bei Bildern (ADR-026).

### 9.2 Neu
```
Telefonbuch-Visitenkarte „💬 Nachricht“
  └─ GET /api/addons/communication/contact/{id} ── prüft beide Seiten ──► URL …/#pbdm:<Principal>
Kommunikation (eigener Tab, ggf. über die Anmeldung)
  └─ GET /api/integration/phonebook/user?subject=… (eigene Sitzung) ──► Konto ──► Direktnachricht

Telefonbuch-Kopf „💬 Kommunikation (n)“
  └─ GET /api/addons/communication/news (60 s, nur sichtbar; 30 s Zwischenspeicher)
       └─ POST /api/integration/phonebook/news {subject} (Schlüssel phonebook->ucng) ──► {dms, mentions}
```

### 9.3 Dateien
| Projekt | Datei | Inhalt |
|---|---|---|
| Telefonbuch | `commcontact.go` (neu) | Kontakt-Endpunkt, Zähler, Zwischenspeicher |
| Telefonbuch | `api.go`, `main.go` | `comm` je Eintrag, Routen |
| Telefonbuch | `web/*` | Knopf „💬 Nachricht“, Zähler am Kopfleisten-Knopf |
| UCNG | `phonebook_news.go` (neu) | `NewsSince`, `…/news`, `…/user` |
| UCNG | `api.go`, `hub.go` | Link-Regel „bekannt“ für Telefonbuch-Konten (`Poster.Known`) |
| UCNG | `phonebook.go`, `web/app.js`, i18n | Hinweise, `#pbdm:` auch über die Anmeldung hinweg |

### 9.4 Ergebnis
Siehe `PH-05-PR.md`.

## 10. PH-06 bis RC: Profil, Medien, Sprache, Betrieb, Sicherheit

### 10.1 Profilbild (PH-06, ADR-015)
- Ablauf:
  1. Das Telefonbuch rendert das Pixel-Avatar im Browser (PNG 128 × 128).
  2. Der Browser schickt es an `PUT /api/addons/communication/avatar`.
  3. Das Telefonbuch speichert es in `comm_avatars`.
  4. UCNG holt es mit `GET /api/internal/communication/avatar/{principal}`, wenn sich der Fingerabdruck (`avatar` in Anmeldung und Nachprüfung) geändert hat.
- Ein eigenes Bild in UCNG hat Vorrang. Wird es entfernt, kommt sofort wieder das Startbild.
- `/api/me` liefert `communication.avatar {source, rendered}`.

### 10.2 Funktionen im Modus `phonebook` (PH-06 bis PH-10)
Alle Zeilen der Matrix sind mit Konten aus dem Telefonbuch geprüft (`phonebook_ops_test.go`, `phonebook_backup_test.go`, Browser `ph06.js`):
- Medien, Sprache, Bühne, Verschieben, Breakout-Räume
- Sprach-Server, temporäre Kanäle, Bots, Moderation, Automoderation
- Löschen von Personendaten, Statistik, Vorlagen, Sprachen, Ankündigungen
- Sicherung und Wiederherstellung

**Angepasst:**
- Externe GIFs und der Beacon sind gesperrt (ADR-009).
- Es gibt kein Widget (ADR-030).
- Transkripte haben eine Frist; die Spracherkennung bleibt bis zur Freigabe aus (ADR-011).

### 10.3 Sicherheit und Betrieb (PH-11, RC)
- **SSRF-Schutz** für Downloads und Webhooks (ADR-029).
- **AD-Nachprüfung** auch im Telefonbuch selbst (ADR-013).
- **Interne Adressen:** http nur für Docker-Dienstnamen (ADR-027).
- **`*_FILE`** für alle UCNG-Geheimnisse; `ucng healthcheck` und `ucng-voice healthcheck`.
- **Referenz-Installation** `deploy/stack/` (ADR-028) mit Gateway, gehärteten Containern, Geheimnissen, Sicherung, Prüfskript und abgesetzten Sprach-Servern.
- **Workflows:**
  - Ein Repository (`phonebook/`, `ucng/`, `deploy/`) mit gemeinsamen Workflows im Wurzelordner: `ci.yml`, `release.yml` (alle Images, Vorabversionen ohne `latest`), `stack.yml`, `cla.yml`, `deps.yml`.
- Prüfbericht: [`docs/security/ph11-review.md`](../security/ph11-review.md); Entscheidungen: [`docs/RELEASE-ENTSCHEIDUNGEN.md`](../RELEASE-ENTSCHEIDUNGEN.md).

### 10.4 Komponenten des Release Candidate

| Komponente | Image / Programm | Quelle | Netz |
|---|---|---|---|
| Telefonbuch | `ghcr.io/b1tsnetwork/phonebook:1.0.0-rc.1` | `Dockerfile` | backend, edge |
| Kommunikation | `ghcr.io/b1tsnetwork/ucng:1.0.0-rc.1` | UCNG `Dockerfile` (`TAGS="sqlite mysql"`) | backend, edge |
| Sprach-Server (abgesetzt) | `ghcr.io/b1tsnetwork/ucng-voice:1.0.0-rc.1` | UCNG `Dockerfile.voice` | eigener Host, `voice-node/` |
| Datenbank | `mysql:8.4` | – | backend |
| Gateway | `caddy:2.10` | `deploy/stack/gateway/Caddyfile` | edge (einzig veröffentlicht) |
| Spracherkennung (aus) | `speaches` (Profil `stt`) | – | backend |
| Datenbank-Sicherung | `mysql:8.4` mit `backup/mysql-backup.sh` (Profil `backup`) | – | backend |

## 11. Nächste Schritte
- **Gate E:** Lizenz- und Markenprüfung (ADR-016), vor jedem Produktivbetrieb.
- **Abnahme vor Ort:**
  - echtes AD;
  - `stack.yml` in GitHub (echte Images, Caddy, TLS);
  - Firefox, Edge und Safari;
  - echte Geräte und Bildschirmfreigabe.
- **Freigabe der Transkription** (ADR-011) – erst dann Profil `stt` und `features.transcription`.
