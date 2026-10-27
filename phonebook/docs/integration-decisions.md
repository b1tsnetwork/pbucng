# Integrationsentscheidungen Phonebook × UCNG (ADR)

**Stand:** 27.10.2026 (PH-00 bis Release Candidate 1.0.0-rc.1) · Entscheidungen des RC gesammelt in [`RELEASE-ENTSCHEIDUNGEN.md`](RELEASE-ENTSCHEIDUNGEN.md) · Bezug: `PHONEBOOK_UCNG_INTEGRATIONSFAHRPLAN.md`, [`docs/architecture/ucng-integration.md`](architecture/ucng-integration.md)

**Status-Werte:**
- **angenommen** – gilt.
- **vorgeschlagen** – empfohlene Richtung, wird in der genannten Phase umgesetzt, sofern der Projektinhaber nicht widerspricht.
- **offen** – Entscheidung des Projektinhabers nötig, bevor die Phase abgeschlossen werden darf.

Sicherheitskritische Punkte werden nicht stillschweigend „irgendwie gelöst“ (Fahrplan §9.9).

| Nr. | Thema | Status | Phase |
|---|---|---|---|
| ADR-001 | Zwei Prozesse, ein Produkt | angenommen | – |
| ADR-002 | Add-on standardmäßig aus | **umgesetzt** | PH-01 |
| ADR-003 | Test-Toolchain und Build-Varianten | angenommen | PH-00 |
| ADR-004 | Subdomain statt Pfad-Präfix | **angenommen** (26.10.2026), umgesetzt | PH-01 |
| ADR-005 | Modus `phonebook` auf Basis des Konto-Modells | **umgesetzt** | PH-02 |
| ADR-006 | Identität und SSO-Ablauf | **umgesetzt** (Ablauf geändert: Authorization Code + PKCE) | PH-02 |
| ADR-007 | E2EE-Entsperrung ohne UCNG-Passwort | **entschieden** (A, 26.10.2026), umgesetzt | PH-02 (Gate B) |
| ADR-008 | Admin- und Moderatorrechte | **entschieden** (b + Inbetriebnahme-Schalter, 26.10.2026), umgesetzt | PH-02 |
| ADR-009 | Betriebssperren für riskante Funktionen | **umgesetzt** | PH-01/RC |
| ADR-010 | Private Medien nach Rechteentzug | **entschieden** (a, 26.10.2026), umgesetzt | PH-03 |
| ADR-011 | Transkripte: Aufbewahrung | **umgesetzt** (Frist); Transkription rechtlich offen, daher aus | RC |
| ADR-012 | Exakte Origin-Prüfung im Phonebook | **umgesetzt** | PH-01 |
| ADR-013 | Gültigkeit von Rechten während einer Sitzung | **angenommen** (26.10.2026), umgesetzt – auch für das Telefonbuch selbst | PH-02/RC |
| ADR-014 | Sprechzeiten vs. UCNG-Öffnungszeiten | **entschieden** (getrennt, nach Empfehlung), umgesetzt | PH-04 |
| ADR-015 | Avatar/Profilbild | **entschieden** (Startbild, 27.10.2026), umgesetzt | PH-06 |
| ADR-016 | Lizenz und Marke | **offen** (Rechtsprüfung) – blockiert den Produktivbetrieb | vor Auslieferung |
| ADR-017 | Pflege der UCNG-Änderungen | **umgesetzt** | ab PH-01 |
| ADR-018 | Ereignisse Phonebook → UCNG | **umgesetzt** – zustandsbasierter Abgleich statt Outbox | PH-02/PH-03 |
| ADR-019 | Keine iframe-Einbettung | **umgesetzt** | PH-04 |
| ADR-020 | Überprüfbare Funktionsschalter | **umgesetzt** | PH-01 |
| ADR-021 | Organisationskanäle und Gruppen | **entschieden** (26.10.2026), umgesetzt | PH-03 |
| ADR-022 | Austritt: sperren, nach Frist anonymisieren | **entschieden** (26.10.2026), umgesetzt | PH-03 |
| ADR-023 | Verwaltung der Kommunikation im Telefonbuch | **umgesetzt** | PH-03 |
| ADR-024 | Navigation, Design-Adapter, Verhalten bei Telefonbuch-Ausfall | **umgesetzt** | PH-04 |
| ADR-025 | Direktnachricht aus der Visitenkarte, Zähler „neu seit letztem Besuch“ | **entschieden** (27.10.2026), umgesetzt | PH-05 |
| ADR-026 | Link-Regel „bekannt“ für Konten aus dem Telefonbuch | **umgesetzt** | PH-05 |
| ADR-027 | Interne Adressen: http nur für Docker-Dienstnamen | **umgesetzt** | RC |
| ADR-028 | Referenz-Installation und Härtung der Container | **umgesetzt** | RC |
| ADR-029 | Schutz ausgehender Verbindungen (SSRF) | **umgesetzt** | RC |
| ADR-030 | Kein Widget für fremde Webseiten im Modus `phonebook` | **umgesetzt** | RC |

---

### ADR-001 – Zwei Prozesse, ein Produkt · *angenommen*
**Kontext:**
- Phonebook nutzt Go 1.24, UCNG Go 1.27.
- UCNG hat eigene Persistenz und In-Memory-Zustände (Hub, Sitzungen, Voice), dazu SSE und WebSockets.
- Beide sind `package main`.

**Entscheidung:** UCNG läuft als eigener Prozess (`ucng-addon`). Die Integration erfolgt über Authentisierung, Berechtigungen, Navigation und Design – nicht durch Zusammenführen der Programme.

**Folgen:**
- Getrennte Volumes, Backups und Releases.
- Gemeinsame Verträge werden versioniert.
- Die Tests beider Projekte laufen bei Vertragsänderungen.

### ADR-002 – Add-on standardmäßig aus · *umgesetzt (PH-01)*
**Entscheidung:** `communication.enabled=false` ist Standard. Ist das Add-on aus:
- kein Menü,
- keine Add-on-Routen (404),
- keine Verbindung zu UCNG,
- kein Einfluss auf `/readyz`.

**Abnahme:** Die Phonebook-Testsuite läuft unverändert ohne UCNG.

### ADR-003 – Test-Toolchain und Build-Varianten · *angenommen*
**Kontext:** Aus dieser Arbeitsumgebung sind `proxy.golang.org` (403) und GitLab nicht erreichbar.

**Entscheidung:**
- Go 1.27.2 wird aus dem offiziellen Tag gebaut.
- Getestet wird UCNG im Standard-Build (ohne Fremdmodule) und mit `-tags mysql` (Build/vet).
- `-tags sqlite` deckt UCNGs eigene GitHub-CI ab.

**Folgen:** SQLite-Befunde aus PH-00 fehlen. Vor Gate A muss die UCNG-CI für alle vier Varianten grün sein.

### ADR-004 – Subdomain statt Pfad-Präfix · *angenommen (Projektinhaber, 26.10.2026), umgesetzt in PH-01*
**Kontext:**
- Das UCNG-Frontend nutzt über 200 absolute `/api/…`-Pfade.
- Hinzu kommen 27 absolute Asset-Pfade in HTML, WebSocket-Adressen aus `location.host`, `/.well-known/ucng` (Föderation, Pflicht an der Wurzel), `/media/…` und `/theme.css`.

**Vorschlag:**
- Erste Ausbaustufe auf einer **eigenen Subdomain**, z. B. `https://kommunikation.<domäne>`, mit gemeinsamer Navigation und SSO.
- Pfadbetrieb (`/communication/`) in PH-01 nur **messen** (automatisierter Routen-Test). Ein Umbau käme als eigener Auftrag.

**Folgen:**
- Zweiter DNS-Name und TLS-Zertifikat.
- Die Cookies/Tokens beider Anwendungen bleiben getrennt (gewollt).
- Der SSO-Austausch läuft per POST über die Origin-Grenze (ADR-006).

**Alternative:** Pfad-Präfix. Dafür wäre ein systematischer Basis-Pfad-Umbau in UCNG nötig, mit größerem Konfliktrisiko bei Upstream-Updates.

**Messung PH-01:**
- 15 absolute Verweise auf der Startseite und 227 in den Skripten;
- 3 WebSocket-Adressen aus `location.host`;
- `/.well-known/ucng` an der Wurzel;
- gespeicherte Bildadressen `/media/uploads/…` in Nachrichten, ein Präfix bräuchte also auch eine Datenmigration.

**Umsetzung:**
- `communication.publicBaseURL` erzwingt eine eigene Herkunft ohne Pfad;
- `telefonbuch commcheck` prüft das Routing über den echten Gateway.

### ADR-005 – Modus `phonebook` auf Basis des Konto-Modells · *umgesetzt (PH-02)*
**Kontext:** Im Modus `simple` dürfen Gäste immer schreiben (`guestPolicy()`, account_api.go:41). Nur `hosted` kennt Konten, Sitzungsverwaltung und Gäste-Regeln.

**Vorschlag:** Neuer Wert `MODE=phonebook`. Er verhält sich wie `hosted`, mit festen Abweichungen:
- Gäste fest `none`;
- Registrierung, Passwort-Login, Passwort-Reset, Einladungen und E-Mail-Bestätigung geschlossen (404/403);
- Kontoanlage nur über den Integrations-Adapter;
- Föderation und Beacon per Betriebssperre aus (ADR-009).

Der Standalone-Betrieb (`simple`, `hosted`) bleibt unverändert und wird weiter getestet.

**Umsetzung (PH-02):** `MODE=phonebook` setzt intern das Konto-Modell von `hosted` und alle Betriebssperren fest (`OpLocks.forPhonebook`); ein abweichendes `LOCK_GUESTS` verhindert den Start. Geschlossen (404, `"phonebook": true`): prelogin, register, login, verify, resend, forgot, reset, invite, password, email, 2fa/*, recovery, keys, federation, delete sowie `POST /api/admin/invites`. Sitzungen ohne Telefonbuch-Bezug (z. B. Passwort-Sitzungen aus der Zeit als `hosted`) gelten nicht mehr. `/api/settings` meldet `phonebook.authorize`; das Telefonbuch gibt die Kommunikation nur frei, wenn diese Adresse auf es selbst zeigt (`commSSOProblems`, auch in `telefonbuch commcheck`).

### ADR-006 – Identität und SSO-Ablauf · *umgesetzt (PH-02), Ablauf gegenüber dem Vorschlag geändert*
**Phonebook:**
- Neue Tabelle `principals(id, provider, external_subject, username, disabled_at, …)`.
- `provider=ldap`: `external_subject` = objectGUID (überlebt Umbenennungen).
- `provider=local`: unveränderliche Zufalls-ID.
- Die Sitzung erhält `PrincipalID`. Gäste haben keinen Principal.

**UCNG:**
- Tabelle bzw. Speicherbereich `external_identities(provider, external_subject UNIQUE, ucng_user_id, last_sync_at, disabled_at)`.
- Schattenkonten haben **kein** Passwort.

**Ablauf:**
1. Browser → `POST /api/addons/communication/launch` (Phonebook, CSRF).
2. Phonebook erzeugt einen einmaligen Code: 30–60 s gültig, gebunden an Principal, Audience, Phonebook-Sitzung und Nonce. Gespeichert wird nur der Hash.
3. Browser → `POST /api/integration/phonebook/exchange` (UCNG, Code im Body).
4. UCNG löst den Code beim Phonebook über einen authentisierten Backend-Endpunkt ein (gemeinsames Secret aus Datei/Secret, mTLS optional). Eine Wiederholung schlägt fehl (einmalig).
5. UCNG legt das Schattenkonto idempotent an bzw. findet es und stellt über `Users.newSessionLocked` eine Sitzung aus. Alle bestehenden Chat-, SSE- und Voice-Pfade akzeptieren sie, ohne geändert zu werden.

**Umgesetzt – Änderung gegenüber dem Vorschlag:** Statt `POST …/launch` aus dem Telefonbuch startet die Anmeldung in UCNG (OAuth-artiger Authorization Code mit PKCE und `state`):
1. UCNG-Seite ohne Sitzung erzeugt `state` und `code_verifier` (sessionStorage, nur dieser Tab) und navigiert zu `GET <Telefonbuch>/api/addons/communication/authorize?state&challenge=S256(verifier)&method=S256`.
2. Das Telefonbuch prüft seine Sitzung (sonst Anmeldung, danach automatisch weiter), Nutzungsrecht und das Sicherheitsprofil von UCNG und leitet mit `#pb_code=…&pb_state=…` (Fragment) zurück. Der Code gilt 60 s, einmal, gebunden an Principal, Telefonbuch-Sitzung und Challenge; gespeichert wird nur sein Hash (im Speicher).
3. UCNG prüft `state`, entfernt das Fragment sofort aus der Adresse und schickt Code + verifier per POST an `/api/integration/phonebook/exchange`.
4. UCNG löst serverseitig ein (`POST /api/internal/communication/redeem`, Bearer = HMAC(Geheimnis, Richtung)). Falscher verifier verbraucht den Code.

*Begründung:* Mit `state` + PKCE lässt sich kein abgefangener oder untergeschobener Code einlösen (Login-CSRF: ein fremder Rücksprung wird im Browser verworfen), der Code erscheint in keinem Server-Protokoll und kein Referer, und abgelaufene UCNG-Sitzungen erneuern sich ohne Klick, solange die Telefonbuch-Sitzung lebt. Auch bei getrennten Registrable Domains (SameSite=Strict) funktioniert es – mit einem Zwischenschritt über die Startseite des Telefonbuchs.

**Identität:** `principals` (Migration 010, JSON-Datei ebenso): `id` = `p_` + 20 Hex (zufällig, unveränderlich), eindeutig über `(provider, subject)`; LDAP: objectGUID (ohne GUID-Attribut ersatzweise `name:<benutzername>` mit Warnung), lokal: Benutzername. Die Sitzung trägt `principal_id`. DEV-Admin und Gäste bekommen keinen Principal. In UCNG steht die Verknüpfung als `member.ext` (Anbieter + Subjekt, Index `byExt`) – kein Abgleich über den Namen; ein gleichnamiges Konto wird nicht übernommen. Anzeigename = Name im Chat, bei Gleichstand „Name (benutzername)“.

**Ausgeschlossen** (Fahrplan §3.2):
- AD-Passwort an UCNG senden;
- `X-User`-Header;
- ein Dienst-Token im Browser;
- Tokens in URLs;
- eine Rechteprüfung nur im Frontend.

### ADR-007 – E2EE-Entsperrung ohne UCNG-Passwort · *entschieden: A (Projektinhaber, 26.10.2026), umgesetzt in PH-02*
**Kontext:** Der private ECDH-Schlüssel liegt verschlüsselt beim UCNG-Server. Den Schlüssel dazu leitet der Browser aus dem UCNG-Passwort ab (`e2e.go`, `web/account.js`). Mit SSO fehlt dieses Passwort.

**Optionen:**

| Option | Beschreibung | Folgen |
|---|---|---|
| **A** – eigenes E2EE-Geheimnis | Beim ersten Öffnen der verschlüsselten DMs wählt die Person ein Entsperr-Geheimnis (Passphrase). Daraus leitet der Browser – wie bisher aus dem Passwort – den Schlüssel ab. Dazu kommen Wiederherstellungscodes. | Kleinste Änderung am bewährten Verfahren (nur die Herkunft des Geheimnisses ändert sich). Mehrgerätebetrieb wie heute. Eine zusätzliche Eingabe beim ersten DM-Zugriff je Gerät/Sitzung. Bei Verlust von Geheimnis **und** Codes sind alte DMs unlesbar (ehrlich anzeigen). |
| **B** – gerätegebundener Schlüssel | Nicht exportierbarer WebCrypto-Schlüssel im Browser-Speicher. Weitere Geräte werden per Kopplung (QR/Code) übertragen. | Keine Eingabe im Alltag. Gerätewechsel, gelöschte Browserdaten und Terminalserver-Profile sind problematisch. Deutlich mehr neuer Code. |
| **C** – vorläufig keine E2EE im Phonebook-Modus | E2EE-DMs sichtbar abgeschaltet (Funktionsschalter). DMs laufen serverseitig wie im Modus `simple`. | Schnell. **Verletzt** aber „E2EE nicht unverschlüsselt ersetzen“, solange nicht ausdrücklich so entschieden – nur als befristete, dokumentierte Ausnahme. |

**Entscheidung:** A. Später optional B als Komfortstufe.

**Umsetzung:** Bedrohungsmodell in [`security/ph02-threat-model.md`](security/ph02-threat-model.md).
- Entsperr-Geheimnis (mind. 10 Zeichen) → PBKDF2-SHA256 (300 000 Runden) → 512 Bit: `auth` (Server, gesalzen gehasht) + Schlüssel für den privaten ECDH-Schlüssel – dasselbe Verfahren wie bisher mit dem Passwort.
- Wiederherstellungscode: 26 Zeichen aus 32 (≈130 Bit), nur einmal angezeigt; verschlüsselt eine zweite Kopie des privaten Schlüssels. Wiederherstellen setzt ein neues Geheimnis, ältere Nachrichten bleiben lesbar, der Code wird ersetzt.
- Änderungen an den Schlüsseldaten verlangen den Nachweis (`auth`) des Geheimnisses bzw. Codes; der öffentliche Schlüssel kann dabei nicht getauscht werden.
- „Schlüssel zurücksetzen“ (beides verloren) nur innerhalb von 15 min nach frischer Anmeldung; Gegenüber sehen den Schlüsselwechsel (TOFU, Sicherheitsnummer).
- Direktnachrichten gibt es im Modus `phonebook` nur verschlüsselt (Klartext 409, keine Uploads) – sonst wären DMs zwischen Personen ohne eingerichtete Schlüssel unverschlüsselt, schwächer als `hosted`.

**Ausgeschlossen:**
- Ableitung aus AD-Passwörtern;
- serverseitige Hinterlegung des Klartextschlüssels;
- eine stille Schlüsselneuvergabe.

### ADR-008 – Admin- und Moderatorrechte · *entschieden: (b), (a) nur zur Inbetriebnahme (Projektinhaber, 26.10.2026), umgesetzt in PH-02*
**Kontext:** UCNG trennt Chat-Personen (`Users`) von Admin-Bereich-Konten (`Accounts`, Rollen admin/moderator, Superadmin über `ADMIN_PASSWORD`). Zwischen beiden gibt es keine Verbindung.

**Optionen:**
- (a) Phonebook-Admins werden automatisch UCNG-Admins (sichtbarer Schalter `phonebookAdminsAreUCNGAdmins`).
- (b) Ausdrückliche Zuordnung unter *Verwaltung → Kommunikation*: Person oder AD-Gruppe → admin/moderator.
- In beiden Fällen meldet das SSO auch im Admin-Bereich an. Das Superadmin-Passwort bleibt als Notfallzugang.

**Entscheidung:** (b) als Dauerlösung, (a) nur zur Inbetriebnahme. Ein Phonebook-Admin ist **nicht** automatisch berechtigt, DMs zu lesen. Das gilt in UCNG schon heute: DMs liest nur der Admin-Bereich, E2EE-DMs niemand.

**Umsetzung:** Konfiguration `communication.roles` (`adminUsers`, `adminGroups`, `moderatorUsers`, `moderatorGroups`, `phonebookAdminsAreUCNGAdmins`; letzterer erzeugt bei jedem Start eine Warnung). Die Rolle kommt bei jedem Einlösen und jeder Nachprüfung neu aus dem Telefonbuch. `POST /api/integration/phonebook/admin` stellt aus der Chat-Sitzung eine Admin-Bereich-Sitzung aus, die nur gilt, solange Chat-Sitzung **und** Zuordnung gelten (`Accounts.LoginBound`). Der Admin-Bereich bietet dafür „Mit Telefonbuch-Konto anmelden“; `ADMIN_PASSWORD` bleibt Notfallzugang. Eine Verwaltungsoberfläche im Telefonbuch folgt mit PH-03 (bis dahin Konfigurationsdatei).

### ADR-009 – Betriebssperren für riskante Funktionen · *umgesetzt (PH-01, abgeschlossen im RC)*
**Kontext:** DM- und Kanal-Föderation sowie der Beacon lassen sich nur im Admin-Bereich zur Laufzeit schalten. Kanal-Föderation gibt es auch im Modus `simple`.

**Vorschlag:**
- UCNG erhält Sperren auf Betriebsebene (z. B. `LOCK_FEDERATION=1`, `LOCK_CHANNEL_FEDERATION=1`, `LOCK_BEACON=1`, `LOCK_EXTERNAL_GIFS=1`). Sie übersteuern die Admin-Einstellungen und werden im Admin-Bereich als „vom Betrieb gesperrt“ angezeigt.
- Das Phonebook-Profil setzt sie standardmäßig.
- Ein Test prüft, dass gesperrte Funktionen 404/403 liefern.

**Stand PH-01** (UCNG `oplocks.go`, Tests `oplocks_test.go`):
- Umgesetzt: `LOCK_GUESTS`, `LOCK_REGISTRATION`, `LOCK_FEDERATION`, `LOCK_CHANNEL_FEDERATION`.
- Änderungsversuche im Admin-Bereich → 409. Sperren, die nicht zum Modus passen → kein Start.
- Zusätzlich gefunden: Ohne Sperren macht UCNG aus einem unbekannten `MODE` still `simple`. Mit Sperren ist das jetzt ein Startfehler.
- Offen (PH-10): `LOCK_BEACON`, `LOCK_EXTERNAL_GIFS`.

**Stand RC:**
- `LOCK_BEACON` und `LOCK_EXTERNAL_GIFS` umgesetzt (`media.go`, `beacon.go`, `i18npacks.go`). Gesperrt sind GIF-Suche bei GIPHY/Tenor, Beacon-Sammlungen, Beacon-Suche und Sprachpakete aus dem Beacon. Antwort: 403 „vom Betrieb gesperrt“.
- Der Modus `phonebook` erzwingt alle sechs Sperren. `/api/settings` meldet sie (`phonebook.locks`). Das Telefonbuch prüft `externalGifs` immer und `beacon`, solange `features.beacon` aus ist; sonst Status „unsicher“.
- Ohne Modus `phonebook` ändert sich nichts (`TestLocksStandaloneUnchanged`).

### ADR-010 – Private Medien nach Rechteentzug · *entschieden: (a) (Projektinhaber, 26.10.2026), umgesetzt in PH-03*
**Kontext:** Bilder aus privaten Kanälen/DMs sind über signierte Adressen erreichbar (uploads.go). Die Signatur ist 1–2 Tage gültig und nicht an eine Person gebunden. Der Fahrplan verlangt den sofortigen Verlust des Zugriffs.

**Optionen:**
- (a) Signatur an die Sitzung binden und die Gültigkeit kurz halten (z. B. 15 min, die Oberfläche lädt nach); beim Widerruf der Sitzung sofort ungültig.
- (b) Abruf nur mit Sitzung, ACL-Prüfung bei jedem Abruf (Kanal-Lesbarkeit); Caching nur `private`.
- (c) Status quo, dokumentierte Restlaufzeit bis 2 Tage.

**Entscheidung:** (a). Bereits heruntergeladene Inhalte lassen sich ohnehin nicht zurückholen.

**Umsetzung (nur Modus `phonebook`, Standalone unverändert):**
- Signatur über Datei **und Kanal**, gültig 15–20 min (5-Minuten-Raster, damit alle Ausgaben dieselbe Adresse haben).
- Abruf nur mit `&v=<Viewer>` einer **gültigen Sitzung**, die den Kanal lesen darf – geprüft bei jedem Abruf (`Cache-Control: private, no-cache`, unverändert → 304). Widerruf, Abmeldung, Austritt oder Wechsel der Einheit sperren sofort.
- Abgelaufene Adressen holt die Oberfläche einmal neu (`GET /api/rooms/{room}/media?src=…`, nur für Bilder, die in diesem Kanal vorkommen).
- Adressen im Admin-Bereich sind nicht an eine Chat-Sitzung gebunden (der Admin-Bereich hat keine), gelten aber ebenfalls nur 15–20 min und sind getrennt signiert. Restrisiko: ein kopierter Admin-Link bleibt bis zu 20 min gültig.

### ADR-011 – Transkripte: Aufbewahrung · *Frist umgesetzt (RC); Transkription rechtlich offen und deshalb aus*
**Kontext:** `TRANSCRIPT_KEEP_DAYS=0` bedeutet heute unbegrenzt.

**Vorschlag:**
- Im Phonebook-Profil ist eine endliche Frist Pflicht (Standard 14 Tage). Ein Start mit `0` wird abgelehnt.
- Transkription bleibt aus, bis Information und Einwilligung organisatorisch geklärt sind (Personalvertretung, Datenschutz).
- Nur eigener STT-Dienst, keine externen Anbieter.
- Das Phonebook speichert keine Transkripte, nur Referenzen.

**Umgesetzt (RC):**
- UCNG im Modus `phonebook`: ohne Angabe 14 Tage (`TRANSCRIPT_KEEP_DAYS`), mehr als 365 Tage → kein Start.
- `/api/settings` meldet die Frist und, ob STT eingerichtet ist.
- Das Telefonbuch meldet „unsicher“, wenn die Frist fehlt oder über `communication.defaultTranscriptRetentionDays` liegt, oder wenn STT ohne `features.transcription` eingerichtet ist.
- Im Stack ist STT ein eigenes, standardmäßig ausgeschaltetes Profil.
- **Offen bleibt die organisatorische Freigabe** (Information, Einwilligung).

### ADR-012 – Exakte Origin-Prüfung im Phonebook · *umgesetzt (PH-01)*
**Befund:** `sameOrigin` (auth.go:309) vergleicht mit `publicURL` per Präfix (`https://tb.example` passt auch auf `https://tb.example.evil`). Das CSRF-Token schützt weiterhin, die Prüfung soll trotzdem exakt sein.

**Entscheidung:**
- Schema, Host und Port vergleichen.
- Beim Referer gilt die Origin des Referers.
- Tests für Präfix-, Port- und Schema-Abweichungen.

### ADR-013 – Gültigkeit von Rechten während einer Sitzung · *angenommen (Projektinhaber, 26.10.2026), umgesetzt in PH-02*
**Kontext:** Phonebook-Sitzungen merken sich `IsAdmin` und die Anmeldeerlaubnis zum Zeitpunkt des Logins. Wer im AD gesperrt wird oder die Gruppe verlässt, bleibt bis `sessionIdle`/`sessionMax` angemeldet. UCNG-Sitzungen würden das übernehmen.

**Vorschlag:**
- (1) UCNG-Sitzungen aus dem SSO laufen kurz (z. B. 8 h, gleitend höchstens bis zum Ende der Phonebook-Sitzung).
- (2) UCNG prüft regelmäßig (z. B. alle 10 min) über den Backend-Kanal, ob der Principal noch aktiv ist. Ist das Phonebook nicht erreichbar: keine Verlängerung über die Restlaufzeit hinaus (fail closed).
- (3) Das Phonebook prüft für angemeldete Integrationsnutzer regelmäßig den AD-Status (Konto deaktiviert, Gruppen).

**Umgesetzt mit diesen Standardwerten (alle einstellbar):**
- UCNG-Sitzung endet spätestens mit der Telefonbuch-Sitzung (`sessionMax`, Feld `Until`).
- UCNG prüft alle **5 min** (`PHONEBOOK_REVALIDATE`, 30 s–1 h) jede Sitzung nach; ohne erfolgreiche Prüfung gilt sie nach **15 min** (`PHONEBOOK_GRACE`) nicht mehr – Streams und Sprachkanal werden getrennt. Kommt das Telefonbuch wieder, gilt sie wieder (nichts wird gelöscht, was das Telefonbuch nicht für ungültig erklärt).
- Nutzung des Chats zählt im Telefonbuch als Aktivität (`sessionIdle` läuft nicht ab, solange der Chat benutzt wird).
- Das Telefonbuch prüft den Verzeichnisstatus (deaktiviert, gelöscht, `userGroupDN`, Gruppen, Admin) für Personen mit Chat-Sitzung höchstens alle **15 min** (`communication.adRecheck`). Ist das AD nicht erreichbar, gilt der letzte Stand **1 h** (`communication.adGrace`), danach fail-closed. Bei Sperre: alle Telefonbuch-Sitzungen der Person weg, Widerruf an UCNG.
- **Ohne Add-on gilt (3) nicht** – das Telefonbuch verhält sich unverändert (ADR-002). Beschlossen: in einer eigenen, späteren Phase auch für das Telefonbuch selbst einführen (eigener Schalter, unabhängig vom Add-on).
- PH-03 ergänzt den Status-Abgleich **aller** verknüpften Personen (auch ohne Sitzung) alle 6 h für den Austritt (ADR-022).

**RC – Nachprüfung für das Telefonbuch selbst** (`adrecheck.go`):
- AD-Sitzungen werden höchstens alle **15 min** geprüft (`auth.adRecheck`, 1 min–24 h), im Hintergrund beim nächsten Aufruf.
- **Gesperrt, gelöscht, ohne Anmeldegruppe:** alle Sitzungen der Person enden, auch in der Kommunikation (Widerruf).
- **Admin-Recht geändert:** die Telefonbuch-Sitzungen enden (neu anmelden mit den neuen Rechten).
- **AD nicht erreichbar:** fail-open – das Telefonbuch bleibt benutzbar. Die Kommunikation sperrt weiter selbst nach `communication.adGrace`.
- Lokale Konten sind nicht betroffen.
- Begründung und Test: [`RELEASE-ENTSCHEIDUNGEN.md`](RELEASE-ENTSCHEIDUNGEN.md) B-6.

### ADR-014 – Sprechzeiten vs. UCNG-Öffnungszeiten · *entschieden: getrennt (PH-04)*
Das Phonebook pflegt Sprechzeiten je Einheit/Aufgabe, UCNG hat globale Öffnungszeiten (Widget/Chat).

**Optionen:**
- getrennt lassen;
- das Phonebook liefert die Zeiten einer zugeordneten Einheit an den Kanal (nur Anzeige).

**Empfehlung:** In PH-04 getrennt lassen und Begriffe klar trennen.

**Entscheidung (nach Empfehlung, Freigabe des Projektinhabers „mit den Empfehlungen fortfahren“):** getrennt.
- **Sprechzeiten** gehören zu Einheiten/Aufgaben und werden nur im Telefonbuch gepflegt und angezeigt.
- **Öffnungszeiten** in UCNG gelten nur für den Chat (Hinweis im Widget außerhalb der Zeiten) und haben mit den Sprechzeiten nichts zu tun. Im Modus `phonebook` steht das als Hinweis im UCNG-Admin-Bereich (*Zeitzone und Öffnungszeiten*).
- Es gibt keinen Abgleich in die eine oder andere Richtung. Eine spätere Anzeige der Sprechzeiten einer Einheit in ihrem Organisationskanal (nur lesend) bleibt möglich und wäre eine eigene Entscheidung.

### ADR-015 – Avatar/Profilbild · *entschieden: Startbild aus dem Telefonbuch (Projektinhaber, 27.10.2026), umgesetzt in PH-06*
**Optionen:**
- Phonebook-Pixel-Avatar als Startbild (serverseitig als PNG gerendert, Benutzer kann überschreiben);
- unabhängiges UCNG-Profilbild.

**Empfehlung:** Startbild aus dem Phonebook, danach unabhängig.

**Entscheidung:** „Das Pixel-Avatar aus dem Telefonbuch wird Startbild in der Kommunikation, und man kann es dort ersetzen.“

**Umsetzung:**
- **Telefonbuch:**
  - Der Browser rendert das Avatar (128 × 128 PNG) und sendet es an `PUT /api/addons/communication/avatar`, wenn sich der Fingerabdruck geändert hat (CSRF, nur eigenes, 409 bei veraltetem Stand).
  - Der Server nimmt nur PNG 128 × 128 an und kodiert neu (Metadaten fallen weg).
  - Speicherung in `comm_avatars` (Migration 011).
- **UCNG:**
  - Holt das Bild über `GET /api/internal/communication/avatar/{principal}` (Schlüssel „ucng->phonebook“), wenn sich der Fingerabdruck in der Anmeldung bzw. Nachprüfung geändert hat.
  - Ein eigenes Bild (Upload) hat Vorrang; Entfernen holt sofort das aktuelle Telefonbuch-Avatar.
- **Tests:** `TestCommAvatar`, `TestPhonebookAvatar`, Browser `ph06.js`.

### ADR-016 – Lizenz und Marke · *offen (Rechtsprüfung)*
**Punkte für die Prüfung:**
- AGPL §13: Wer eine geänderte UCNG-Fassung Personen über das Netz anbietet (auch Beschäftigten), muss ihnen den Quellcode anbieten – `SOURCE_URL` setzen und die Quelle bereitstellen.
- Das Phonebook bleibt ein eigenständiges Programm, das über HTTP mit UCNG spricht; seine Lizenz ist davon zunächst getrennt zu betrachten.
- Das CLA regelt Beiträge Dritter.
- `TRADEMARKS.md`: Nutzung von Namen und Logo „UCNG“ in der integrierten Oberfläche.

**Folge:** Keine Auslieferung vor dokumentierter Prüfung (Gate E). Dieses Dokument ist keine Rechtsberatung.

**Stand RC:**
- Die Release-Workflows von UCNG setzen `SOURCE_URL` auf Repository und Commit (Images und Programmdateien). Für AGPL §13 muss diese Adresse für alle Nutzerinnen und Nutzer erreichbar sein (bei einem privaten Repository: Zugang oder eine öffentliche Quelle).
- Der RC ist für Abnahme und Test bestimmt, nicht für den Produktivbetrieb, solange diese Prüfung fehlt.

### ADR-017 – Pflege der UCNG-Änderungen · *umgesetzt*
**Vorschlag:**
- UCNG-Änderungen im Branch `phonebook-integration` des UCNG-Repositories.
- Integrationscode in **eigenen Dateien** (`phonebook_*.go`, `web/phonebook-*.js`) mit wenigen, klar markierten Haken im Bestandscode (Modus-Prüfung, Sitzungsausgabe, Betriebssperren).
- Jede Phase rebast auf `main`.
- Die Paritätsmatrix gilt für beide Modi.

**Stand RC:** Alle Änderungen liegen im Branch `phonebook-integration` als Patch-Folge PH-01 … RC. Neuer Code steht in eigenen Dateien (`phonebook*.go`, `oplocks.go`, `netguard.go`, `internal/healthcheck`). Im Bestandscode gibt es Haken mit Kommentar. Ohne `MODE=phonebook` verhält sich UCNG wie vorher; das prüfen die Tests mit dem Zusatz „Standalone“ bzw. „Unchanged“. Vorschlag für das Tag: `v1.0.0-rc.1` auf dem Branch.

### ADR-018 – Ereignisse Phonebook → UCNG · *umgesetzt – zustandsbasiert statt Outbox (PH-03)*
**Vorschlag:** Abmeldung, Sperre, Rechte- und OrgUnit-Änderungen schreibt das Phonebook in eine Outbox (versioniert, idempotent). Ein Worker überträgt sie signiert an UCNG und wiederholt bei Fehlern. Ergänzend gleicht UCNG periodisch ab (ADR-013).

Bei widersprüchlicher Identität: fail closed.

**PH-02:** Abmeldung (Sitzung) und Sperre (Person) gehen sofort per `POST /api/integration/phonebook/revoke` an UCNG – Warteschlange im Speicher, bis zu 8 Versuche mit wachsendem Abstand. Geht ein Widerruf verloren (z. B. Neustart), beendet die nächste Nachprüfung durch UCNG die Sitzung (≤ 5 min).

**PH-03 – Änderung gegenüber dem Vorschlag:** Statt einer persistenten Outbox mit Einzelereignissen gleicht jeder Teil seinen **Gesamtstand** ab:
- Organisation: `PUT /api/integration/phonebook/org` mit allen Einheiten und einer Version (Hash) – bei Änderung (Prüfung jede Minute) und spätestens alle 30 min; idempotent.
- Gruppen und Rollen einer Person: bei jeder Anmeldung und Nachprüfung (5 min) in den Identitätsdaten.
- Austritt: Status-Abfrage aller verknüpften Personen (6 h).
- Widerruf: sofort (Warteschlange) und abgesichert durch die Nachprüfung.

*Begründung:* Ein verlorenes, doppeltes oder vertauschtes Ereignis kann keinen falschen Dauerzustand erzeugen; nach jedem Ausfall stellt der nächste Abgleich den richtigen Stand her. Eine Outbox bräuchte zusätzlich Versionierung, Reihenfolge und Wiederanlauf – ohne Sicherheitsgewinn. Längste Verzögerung: Organisation ≤ 1 min, Rechte einer Person ≤ 5 min, Austritt ≤ 6 h (Sitzungen enden bei AD-Sperre aber sofort bzw. ≤ 5 min).

### ADR-019 – Keine iframe-Einbettung · *umgesetzt (PH-04)*
**Kontext:**
- Das Phonebook verbietet Framing (`frame-ancestors 'none'`, `frame-src 'self'`).
- Die UCNG-Chatseite lässt sich derzeit von jeder Seite einbetten (kein `frame-ancestors`).
- Konferenzen brauchen Kamera, Mikrofon und Bildschirmfreigabe.

**Vorschlag:**
- Kommunikation öffnet als eigenständige Seite auf der UCNG-Subdomain, mit gemeinsamer Kopfleiste/Navigation (Design-Adapter) und Rücksprung ins Phonebook.
- UCNG erhält `frame-ancestors 'self'` für die Chatseite – Clickjacking-Schutz, Prüfung in PH-11. Das Widget ist davon nicht betroffen, weil es per Skript eingebunden wird.

**Umgesetzt:**
- Im Modus `phonebook` senden alle UCNG-Antworten `frame-ancestors 'self'` (CSP) und `X-Frame-Options: SAMEORIGIN`; `/admin` bleibt bei `DENY`. Ohne Modus `phonebook` bleibt UCNG unverändert (Standalone darf weiterhin eingebettet werden).
- Das Widget-Skript (`/embed/*`, CORS `*`) ist nicht betroffen.
- `telefonbuch commcheck` prüft das (Punkt „Kommunikation: keine Einbettung“) – ein UCNG-Stand vor PH-04 oder ein Proxy, der die Kopfzeile entfernt, fällt durch.
- Geprüft im Browser: eine fremde Seite kann die Chatseite nicht einbetten, die eigene Seite schon (Gegenprobe).

### ADR-020 – Überprüfbare Funktionsschalter · *umgesetzt (PH-01)*
**Kontext:** Fahrplan-Kriterium 4 verlangt „deklarierte, überprüfbare Funktionsschalter“. Ein Schalter nur im Phonebook-Menü genügt nicht (§7).

**Entscheidung:**
- Das Phonebook prüft regelmäßig die öffentlichen UCNG-Einstellungen (`/api/settings`): Modus, Gäste-Regel, Registrierung, DM- und Kanal-Föderation, Voice.
- Weicht UCNG vom Sicherheitsprofil ab, gilt die Kommunikation als `insecure` und wird **nicht freigegeben**.
- Hinweise nennen die fehlende `LOCK_…`-Variable; Metrik `tb_communication_secure`.
- `telefonbuch commcheck` prüft dasselbe zusätzlich von außen durch den Gateway, inklusive tatsächlicher Abweisung ohne Anmeldung.

**Folgen:** Ein versehentlich eingesetztes Standard-Image ohne Sperren öffnet keinen Gastzugang im integrierten Produkt. Weitere Schalter (Medien, temporäre Räume, Transkription, Bots) werden in ihren Phasen ebenso überprüfbar gemacht.

### ADR-021 – Organisationskanäle und Gruppen · *entschieden (Projektinhaber, 26.10.2026), umgesetzt in PH-03*
**Entscheidung:** Zum Kanal einer Einheit gehören alle Personen der Einheit **und ihrer Untereinheiten**; Leitung und Vertretung moderieren ihn.

**Umsetzung:**
- Gruppen je Person (Identitätsdaten): `org:<id>` für die eigene Einheit und alle Oberheiten, `lead:<id>` für Leitung/Vertretung – fest eingetragen **oder** über das Leitungs-Schlagwort der Ebene (wie in der Organisationsansicht). Nur aktive, nicht ausgeblendete Einträge; die Verknüpfung läuft über Konto ↔ Eintrag (Benutzername).
- In UCNG wirken sie als Zugriffsrollen `pb:org:<id>`/`pb:lead:<id>`. Admins können sie auch für eigene private Kanäle wählen (Kanal-Editor, „Einheiten aus dem Telefonbuch“). Unbekannte Gruppen werden verworfen.
- Kanäle: `communication.autoCreateOrgRooms` und optional `orgRoomLevels` (z. B. nur Ämter). Name aus Kürzel + Name (`32-ordnungsamt`), Beschreibung = Pfad, Gruppe „Organisation“. Lesen/Schreiben `pb:org:<id>`, Moderation (Löschen, Anpinnen – kein globales Stummschalten) `pb:lead:<id>`. Umbenennung der Einheit zieht den automatisch vergebenen Namen nach, einen vom Admin vergebenen nicht.
- Fällt eine Einheit weg (oder ohne Kanal): Kanal wird **archiviert** (nur noch Team/Admin-Bereich), nichts gelöscht. Entfernte Gruppen machen Kanäle nie öffentlich (nur noch Team).
- Ändern sich die Gruppen einer Person, trennt UCNG sofort die Live-Verbindungen zu Kanälen, die sie nicht mehr lesen darf.
- Team-Abzeichen werden **nicht** aus dem Telefonbuch vergeben: Das Team liest alle Kanäle mit Rollen, auch Organisationskanäle.

### ADR-022 – Austritt: sperren, nach Frist anonymisieren · *entschieden (Projektinhaber, 26.10.2026), umgesetzt in PH-03*
- Meldet das Telefonbuch eine Person als gesperrt, gelöscht oder ohne Nutzungsrecht (Nachprüfung, Widerruf oder Status-Abgleich alle `PHONEBOOK_LIFECYCLE` = 6 h), wird ihr Konto sofort gesperrt: alle Sitzungen enden, keine Rolle, keine Gruppen.
- Rückkehr innerhalb der Frist (wieder freigegeben, neue Anmeldung): gleiches Konto.
- Nach `PHONEBOOK_RETENTION_DAYS` (Standard **90**, 1–3650) wird anonymisiert: Name → „Ehemalige Person …“ (auch in allen Nachrichten und Reaktionen), Profilbild, Status, Rollen, Schlüssel, Sitzungen und die Verknüpfung werden gelöscht. Die Nachrichten bleiben. Danach bekommt eine zurückkehrende Person ein neues Konto.
- Ist das Telefonbuch nicht erreichbar, wird nichts gesperrt oder gelöscht (die Sitzungen sichert die Frist aus ADR-013 ab).

### ADR-023 – Verwaltung der Kommunikation im Telefonbuch · *umgesetzt in PH-03*
- Menü *Verwaltung → Kommunikation verwalten* (nur Admins): Status, letzter Organisationsabgleich, Nutzungsrecht (Benutzer, Gruppen, lokale Konten), Rollen (ADR-008), Organisationskanäle und Ebenen, „Organisation abgleichen“.
- Gespeicherte Einstellungen überschreiben die Konfigurationsdatei (Einstellung `communicationPolicy`, protokolliert), „Auf Konfigurationsdatei zurücksetzen“ entfernt sie. Ein ungültig gespeicherter Stand wird nicht angewendet (es gilt die Datei).
- Verbindung, Adressen und Geheimnis bleiben ausschließlich in der Konfigurationsdatei.

### ADR-024 – Navigation, Design-Adapter, Verhalten bei Telefonbuch-Ausfall · *umgesetzt in PH-04*
**Navigation:**
- Telefonbuch: Knopf „💬 Kommunikation“ in der Kopfleiste (zusätzlich zum Menüpunkt), gesperrt mit Grund, wenn das Nutzungsrecht fehlt; für Gäste unsichtbar. Öffnet die Kommunikation in einem eigenen Tab (Name `tb-communication`).
- Kommunikation: Link „‹ Telefonbuch“ unter dem Titel in der Seitenleiste; auf Profilkarten „Im Telefonbuch“ (Suche nach dem Anzeigenamen aus dem Telefonbuch, `?q=…&from=chat`). Beide öffnen einen neuen Tab mit `noopener` – der Chat (z. B. ein laufendes Gespräch) bleibt offen, und keine Seite kann die andere umleiten.
- Ein eigenes Tab statt Rücksprung im selben Tab, weil das Telefonbuch `Cross-Origin-Opener-Policy: same-origin` sendet: benannte Fenster lassen sich über die Subdomains hinweg nicht wiederverwenden. Mit `from=chat` übergibt das Telefonbuch die Suche nicht an ein anderes offenes Telefonbuch-Fenster (sonst schlösse sich der neue Tab sofort wieder).
- Den Link „Im Telefonbuch“ gibt es nur für Konten aus dem Telefonbuch (UCNG liefert dafür `pb` in `/api/profiles`, nur angemeldet, nicht nach einem Austritt).

**Design-Adapter:** Stehen Titel, Akzent- und Seitenleistenfarbe von UCNG noch auf Werkseinstellung, übernimmt UCNG im Modus `phonebook` beim Start einmalig Titel „Kommunikation“ und die Akzentfarbe des Telefonbuchs (`#1f6f8b`) und speichert sie. Danach gilt allein, was im UCNG-Admin-Bereich eingestellt ist; geänderte Werte werden nie überschrieben.

**Ausfall des Telefonbuchs (Befund im Ende-zu-Ende-Test, ergänzt ADR-006/T8):**
- Bisher antwortete UCNG nach Ablauf der Frist mit 401. Die Oberfläche hielt das für „abgemeldet“, verwarf das Token, und die Sitzung kam nach dem Ausfall nicht zurück, obwohl sie serverseitig wieder gültig wurde. Der PH-02-Test war deshalb zeitabhängig.
- Jetzt: Für eine Sitzung, die *nur* wegen der ausgebliebenen Nachprüfung nicht gilt, antwortet UCNG mit **503** (`phonebookDown: true`, `Retry-After`). Der Zugriff bleibt gesperrt (fail-closed, nichts wird ausgeliefert oder angenommen). Widerrufene, abgelaufene oder gesperrte Sitzungen bekommen weiterhin 401.
- Die Oberfläche behält bei 503 die Sitzung, zeigt „Das Telefonbuch ist gerade nicht erreichbar. Deine Sitzung bleibt erhalten“ (auch nach Neuladen, statt zum Telefonbuch umzuleiten) und arbeitet nach der nächsten erfolgreichen Nachprüfung ohne neue Anmeldung weiter.
- Preisgegeben wird dadurch nur, dass ein vorgelegtes Token zu einer bestehenden, nicht widerrufenen Sitzung gehört – das kann nur, wer das Token hat.

### ADR-025 – Direktnachricht aus der Visitenkarte, Zähler „neu seit letztem Besuch“ · *entschieden 27.10.2026, umgesetzt in PH-05*
**Direktnachricht aus der Visitenkarte:**
- Die Visitenkarte zeigt „💬 Nachricht“, wenn der Eintrag mit einem Benutzerkonto verknüpft ist, aktiv und nicht ausgeblendet ist und die angemeldete Person die Kommunikation nutzen darf (nicht für sich selbst, nicht für Gäste).
- Ein Klick fragt `GET /api/addons/communication/contact/{id}`. Das Telefonbuch prüft dabei beide Seiten (Nutzungsrecht, Sperre) und liefert die Adresse der Kommunikation mit `#pbdm:<Principal>`.
- Die Kommunikation löst den Principal mit der eigenen Sitzung auf (`GET /api/integration/phonebook/user`), entfernt ihn sofort aus der Adresse und öffnet die (verschlüsselte) Direktnachricht. Auch eine nötige Anmeldung über das Telefonbuch geht dabei nicht verloren.
- Wer sich noch nie am Telefonbuch bzw. in der Kommunikation angemeldet hat, bekommt keine leere Unterhaltung, sondern einen Hinweis. Hat das Gegenüber die Verschlüsselung noch nicht eingerichtet, sagt die Kommunikation das.

**Zähler im Telefonbuch-Kopf – Entscheidung des Projektinhabers: „neu seit letztem Besuch“.** Abgelehnt wurden:
- ein serverseitiger Gelesen-Stand (neue personenbezogene Daten: wer hat was wann gelesen);
- gar kein Zähler.

**Umsetzung des Zählers:**
- Gezählt werden Direktnachrichten und Erwähnungen (@Name, @Rolle) anderer Personen, die nach der letzten Aktivität im Chat eingegangen sind. Nicht gezählt werden eigene, gelöschte, von blockierten Personen und solche aus Kanälen ohne Leserecht.
- „Letzte Aktivität“ ist der vorhandene Zeitstempel „zuletzt gesehen“. Er wird höchstens einmal je Minute aktualisiert; Nachrichten aus der letzten Minute vor einem Besuch können deshalb noch mitgezählt werden.
- Der Gelesen-Stand bleibt wie bisher im Browser. UCNG speichert dafür nichts Neues.
- Das Telefonbuch fragt UCNG serverseitig (`POST /api/integration/phonebook/news`, Schlüssel „phonebook->ucng“). Zurück kommen nur zwei Zahlen – keine Inhalte, Absender oder Kanäle. Das Telefonbuch hält sie 30 s im Speicher (gegen Last durch viele offene Fenster) und speichert nichts dauerhaft.
- Die Oberfläche fragt alle 60 s, nur bei sichtbarem Fenster.

### ADR-026 – Link-Regel „bekannt“ für Konten aus dem Telefonbuch · *umgesetzt in PH-05*
- Kanäle mit der Link-Regel „bekannt“ ließen Links bisher erst zu, wenn der Name älter als 24 Stunden war. Die Regel richtet sich gegen frisch reservierte Namen ohne Konto.
- Im Modus `phonebook` gelten Konten aus dem Telefonbuch als bekannt – wie schon bei der Upload-Regel (PH-03). Sonst dürften neue Kolleginnen und Kollegen am ersten Tag keine Links teilen, obwohl sie über das AD identifiziert sind.
- Standalone bleibt unverändert (Test `TestLinksKnownStandaloneUnchanged`).

### ADR-027 – Interne Adressen: http nur für Docker-Dienstnamen · *umgesetzt (RC)*
**Kontext:** Beim Start des Compose-Stacks lehnte UCNG `PHONEBOOK_INTERNAL_URL=http://app:8080` ab („HTTPS ist Pflicht“). Die Anleitung seit PH-01 hätte so nicht gestartet. Das Telefonbuch erlaubte für `internalBaseURL` dagegen jedes http.

**Entscheidung:**
- Die **interne** Adresse darf http nutzen bei `localhost` oder einem Host **ohne Punkt**. Das ist ein Dienstname im selben Docker-Netz; der Verkehr bleibt auf dem Host.
- IP-Adressen und Namen mit Punkt brauchen HTTPS.
- Die **öffentliche** Adresse braucht immer HTTPS (außer `localhost`).

**Test:** `TestPhonebookConfig` (Dienstname, Name mit Punkt, IPv4, IPv6, öffentlich), Mutationstest, Stack-Lauf.

### ADR-028 – Referenz-Installation und Härtung der Container · *umgesetzt (RC)*
`deploy/stack/` enthält Compose, Gateway (Caddy), Geheimnisse, Sicherung, Prüfskript und `voice-node/` für abgesetzte Sprach-Server.
- **Netz:** Nur der Gateway ist erreichbar. Er hat eine feste Adresse, und nur sie darf `X-Forwarded-For` setzen (`/32`); andere Container bekommen Adressen aus `ip_range`.
- **Container:** Ohne Root, schreibgeschützt, `no-new-privileges`, `cap_drop: ALL`.
- **Geheimnisse:** Nur als Dateien (`*_FILE`, `0440 root:65532`).
- **Prüfung:** `check.sh` (`telefonbuch commcheck` durch den Gateway).
- Einzelheiten: [`RELEASE-ENTSCHEIDUNGEN.md`](RELEASE-ENTSCHEIDUNGEN.md) B-8/B-9 und [`deploy/stack/README.md`](../../deploy/stack/README.md).

### ADR-029 – Schutz ausgehender Verbindungen (SSRF) · *umgesetzt (RC)*
**Kontext:** Im Modus `phonebook` haben auch Personen Admin-Rechte, die das Telefonbuch zuordnet. Über „Emote/GIF per Adresse“ oder Bot-Webhooks könnten sie Anfragen ins interne Netz auslösen.

**Entscheidung:** `netguard.go` prüft die tatsächlich verbundene Adresse.
- **Immer gesperrt:** Loopback, Link-Local, Multicast.
- **Private Netze:** für Downloads nur mit `OUTBOUND_ALLOW_PRIVATE=1`; für Webhooks erlaubt.
- Weiterleitungen und Proxy werden mitgeprüft.

**Test:** `TestBlockedAddr`, `TestPhonebookOutboundGuard`.

### ADR-030 – Kein Widget für fremde Webseiten im Modus `phonebook` · *umgesetzt (RC)*
Das Widget ist für anonyme Gäste gebaut; im Modus `phonebook` gibt es keine Gäste.
- Anlegen und Ändern → 403, Vorlagen mit Widgets → abgelehnt.
- Fremde Herkünfte (CORS) nur mit `ALLOWED_ORIGINS`.
- Der Admin-Bereich blendet Widgets, Beacon und Kanal-Abos aus.

**Test:** `TestPhonebookMediaAndLocks/kein Widget…`, `TestPhonebookTemplateNoWidgets`.
