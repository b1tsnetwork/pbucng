# Kommunikations-Add-on (UCNG) bereitstellen – Minimalweg

> **Empfohlen für den Release Candidate:** die Komplett-Installation [`deploy/stack/`](../../../deploy/stack/README.md) mit Gateway, gehärteten Containern, Geheimnissen, Sicherung und abgesetzten Sprach-Servern. Diese Seite beschreibt den Minimalweg über `compose.communication.yml`.

Das Add-on ist **optional**. Ohne die Schritte hier läuft das Telefonbuch unverändert (ADR-002).

Stand:
- eigene Subdomain, UCNG ohne Gastzugang, Statusprüfung, Routing-Test (PH-01);
- **gemeinsame Anmeldung über das Telefonbuch** (PH-02): UCNG läuft mit `MODE=phonebook`, ein eigenes Passwort, Registrierung oder Einladungen gibt es dort nicht mehr. Im Telefonbuch-Menü erscheint „💬 Kommunikation“.
- **Organisation und Rechte** (PH-03): Kanäle je Einheit, Einheiten als Zielgruppen für private Kanäle, Moderation durch Leitung/Vertretung, kurzlebige Bildadressen, Austritt mit Anonymisierung nach Frist, Verwaltung unter *Verwaltung → Kommunikation verwalten*.
- **Visitenkarte und Zähler** (PH-05): „💬 Nachricht“ auf der Visitenkarte, Zähler neuer Direktnachrichten/Erwähnungen am Knopf „Kommunikation“.
- **Einstieg und Navigation** (PH-04): Knopf „💬 Kommunikation“ in der Kopfleiste, Rücksprung ins Telefonbuch, keine Einbettung in fremde Seiten, Erscheinungsbild des Telefonbuchs.

## 1. UCNG-Image mit Betriebssperren und Modus phonebook

Sperren (`oplocks.go`) und Modus `phonebook` (`phonebook*.go`) stehen im UCNG-Branch `phonebook-integration`. Bis sie in einem offiziellen Release sind, das Image selbst bauen:

```bash
git -C ucng checkout phonebook-integration
docker build -t ucng:phonebook-integration ucng
```

Ein Image ohne Sperren läuft zwar, aber das Telefonbuch meldet es als `insecure` und gibt die Kommunikation nicht frei.

## 2. DNS und Zertifikat
- Eigener Name, z. B. `kommunikation.firma.local`.
- Eigenes TLS-Zertifikat. Kamera und Mikrofon brauchen HTTPS.

## 3. Geheimnis und Umgebungsdateien

```bash
mkdir -p secrets
openssl rand -base64 48 > secrets/ucng_integration_secret
chown 65532:65532 secrets/ucng_integration_secret     # Benutzer/Gruppe „nonroot“ im Telefonbuch-Image
chmod 440 secrets/ucng_integration_secret              # nicht für alle lesbar (wird beim Start geprüft)
cp deploy/communication/ucng.env.example deploy/communication/ucng.env   # ADMIN_PASSWORD, SOURCE_URL …
chmod 600 deploy/communication/ucng.env
```

`.env` (neben `docker-compose.yml`) ergänzen:

```
UCNG_PUBLIC_URL=https://kommunikation.firma.local
TB_PUBLIC_URL=https://telefonbuch.firma.local      # muss zu publicURL des Telefonbuchs passen
UCNG_IMAGE=ucng:phonebook-integration
UCNG_TRANSCRIPT_KEEP_DAYS=14
```

## 4. Gateway
Vorlagen für nginx und Caddy liegen in [`deploy/gateway/`](../gateway/). Wichtig für UCNG:
- `proxy_buffering off` – für Server-Sent Events;
- WebSocket-Upgrade weiterreichen;
- `client_max_body_size 10m`.

## 5. Starten

```bash
docker compose -f docker-compose.yml -f compose.communication.yml --profile communication up -d
# optional zusätzlich: --profile voice (weiterer Sprach-Server), --profile stt (Spracherkennung, erst ab PH-09)
```

Fest in `compose.communication.yml`, nicht in der `.env`, damit es nicht versehentlich verloren geht:

| Variable | Wirkung |
|---|---|
| `MODE=phonebook` | Anmeldung nur über das Telefonbuch; setzt automatisch: kein Gastzugang, keine Registrierung, keine Föderation, keine Kanal-Föderation |
| `PHONEBOOK_URL` | öffentliche Adresse des Telefonbuchs (aus `TB_PUBLIC_URL`) |
| `PHONEBOOK_INTERNAL_URL=http://app:8080` | Server-zu-Server im Docker-Netz |
| `PHONEBOOK_SECRET_FILE` | dasselbe Geheimnis wie im Telefonbuch |
| `PHONEBOOK_REVALIDATE` / `PHONEBOOK_GRACE` | Nachprüfung (Standard 5m) / Frist ohne Prüfung (15m), siehe ADR-013 |

Der Admin-Bereich zeigt diese Felder als „vom Betrieb gesperrt“ und lehnt Änderungen ab (409).

UCNG startet **nicht**, wenn die Sperren nicht zur Betriebsart passen (z. B. `LOCK_GUESTS=read` mit `MODE=phonebook`, Tippfehler in `MODE`) oder `PHONEBOOK_*` fehlen bzw. das Geheimnis für alle lesbar ist.

## 5a. Anmeldung, Rechte und Verschlüsselung (PH-02)

Im Telefonbuch (`config.json`, Block `communication`):

```json
"access": { "users": [], "groups": [], "allowLocal": true },
"roles": { "adminUsers": ["it-admin"], "adminGroups": [], "moderatorUsers": [], "moderatorGroups": ["CN=Chat-Moderation,OU=Gruppen,DC=firma,DC=local"],
           "phonebookAdminsAreUCNGAdmins": false },
"adRecheck": "15m", "adGrace": "1h"
```

- **access** leer = alle angemeldeten Personen (außer DEV-Admin). Gäste des öffentlichen Telefonbuchs haben nie Zugang.
- **roles**: Admin- und Moderationsrechte in UCNG nur durch ausdrückliche Zuordnung (ADR-008). `phonebookAdminsAreUCNGAdmins` nur zur Inbetriebnahme einschalten (Warnung im Log), danach Admins eintragen und wieder ausschalten. `ADMIN_PASSWORD` bleibt der Notfallzugang zu `/admin`.
- **adRecheck/adGrace**: Verzeichnisstatus (deaktiviert, gelöscht, `ldap.userGroupDN`, Gruppen) wird für Personen mit Chat-Sitzung so oft nachgeprüft; ist das AD nicht erreichbar, gilt der letzte Stand bis `adGrace`.
- `ldap.attributes.guid` sollte gesetzt sein (Standard `objectGUID`) – sonst verknüpft die Kommunikation über den Benutzernamen.
- Gateway: `/api/internal/` des Telefonbuchs nicht nach außen geben (Vorlagen tun das).

**Verschlüsselte Direktnachrichten:** Beim ersten Öffnen legt jede Person ein eigenes Entsperr-Geheimnis fest (nicht das Windows-Passwort) und bekommt einen Wiederherstellungscode. Auf neuen Geräten wird mit dem Geheimnis entsperrt. Direktnachrichten gehen nur verschlüsselt. Die IT kann vergessene Geheimnisse **nicht** zurückholen – ohne Geheimnis und Code bleibt nur „Schlüssel zurücksetzen“ (ältere verschlüsselte Nachrichten werden unlesbar).

**Abmelden:** Abmelden im Telefonbuch beendet auch die Sitzung in der Kommunikation (sofort, spätestens nach `PHONEBOOK_REVALIDATE`).

## 5b. Organisation und Austritt (PH-03)

- **Organisationskanäle:** `communication.autoCreateOrgRooms: true`, optional `orgRoomLevels: [2]` (nur Ebene 2, z. B. Ämter). Zum Kanal einer Einheit gehören die Personen der Einheit und ihrer Untereinheiten (Zuordnung Konto ↔ Eintrag über den Benutzernamen); Leitung und Vertretung moderieren. Fällt eine Einheit weg, wird ihr Kanal archiviert (nur noch Team).
- **Eigene private Kanäle:** Im UCNG-Admin-Bereich lassen sich Einheiten im Kanal-Editor als Zielgruppe wählen („Einheiten aus dem Telefonbuch“).
- **Verwaltung:** *Verwaltung → Kommunikation verwalten* im Telefonbuch: Status, Nutzungsrecht, Rollen, Organisationskanäle, „Organisation abgleichen“. Dort gespeicherte Einstellungen überschreiben die Konfigurationsdatei, bis sie zurückgesetzt werden.
- **Austritt:** Gesperrte/gelöschte Personen werden sofort gesperrt; nach `PHONEBOOK_RETENTION_DAYS` (Standard 90) anonymisiert UCNG das Konto (Name in allen Nachrichten → „Ehemalige Person …“, Profil und Schlüssel gelöscht). `PHONEBOOK_LIFECYCLE` (Standard 6h) bestimmt, wie oft alle Konten abgeglichen werden.
- **Bilder** aus privaten Kanälen und DMs gelten nur 15–20 Minuten und nur mit der eigenen Sitzung; die Oberfläche lädt abgelaufene Adressen selbst nach.

## 5c. Einstieg, Navigation, Erscheinungsbild (PH-04)

- **Kein iframe:** Die Kommunikation lässt sich nicht in andere Seiten einbetten (`frame-ancestors 'self'`, `X-Frame-Options: SAMEORIGIN`). Der Reverse-Proxy darf diese Kopfzeilen nicht entfernen oder überschreiben – `commcheck` prüft das.
- **Erscheinungsbild:** Beim ersten Start im Modus phonebook setzt UCNG Titel „Kommunikation“ und die Akzentfarbe des Telefonbuchs, sofern im UCNG-Admin-Bereich noch nichts geändert wurde. Eigene Werte dort haben immer Vorrang.
- **Öffnungszeiten** (UCNG-Admin) gelten nur für den Chat bzw. das Widget, nicht für die Sprechzeiten der Einheiten im Telefonbuch.
- **Telefonbuch-Ausfall:** Nach `PHONEBOOK_GRACE` ohne Nachprüfung ist der Chat gesperrt (HTTP 503, Hinweis „Das Telefonbuch ist gerade nicht erreichbar“). Die Sitzungen bleiben erhalten; ist das Telefonbuch zurück, geht es ohne neue Anmeldung weiter (spätestens nach `PHONEBOOK_REVALIDATE`).

## 5d. Visitenkarte und Zähler (PH-05)

- **„💬 Nachricht“** erscheint auf Visitenkarten von Einträgen mit Benutzerkonto (Feld *Benutzername* in den Stammdaten), wenn beide die Kommunikation nutzen dürfen. Die Person muss sich mindestens einmal am Telefonbuch angemeldet haben, und für Nachrichten auch einmal in der Kommunikation (Verschlüsselung einrichten).
- **Zähler:** Das Telefonbuch fragt UCNG über die interne Adresse (`communication.internalBaseURL`) nach der Zahl neuer Direktnachrichten und Erwähnungen. Das läuft wie der Organisationsabgleich direkt von Server zu Server (im Compose-Netz), nicht über den Gateway. Gespeichert wird nichts – der Gelesen-Stand bleibt im Browser.
- **Link-Regel „bekannt“** in Kanälen: Konten aus dem Telefonbuch gelten sofort als bekannt.

## 6. Prüfen (Gate A, ab PH-02 inkl. Modus phonebook)

Am besten von einem Admin-Rechner aus, also von außen durch den Gateway:

```bash
telefonbuch commcheck \
  -phonebook https://telefonbuch.firma.local -communication https://kommunikation.firma.local \
  -ucng-admin-password-file ./ucng-admin-pw.txt    # optional: prüft dann auch SSE ohne Proxy-Puffer
```

Weitere Optionen:
- `-ca firma-root.pem` – interne Zertifikate;
- `-connect-to host:port` – einen Gateway vor der DNS-Umstellung prüfen.

Im Container geht es auch: `docker compose exec app /telefonbuch commcheck …`.

Geprüft werden:
- getrennte Herkunft;
- Telefonbuch erreichbar, Add-on aktiv;
- UCNG-Startseite mit CSP;
- Sicherheitsprofil;
- `MODE=phonebook` mit der Adresse dieses Telefonbuchs;
- keine Einbettung (`frame-ancestors`, ab PH-04);
- `/.well-known/ucng` ohne Föderation;
- kein Gastzugang (Kanäle, Verlauf, SSE, Profile, Gastname, Registrierung);
- WebSocket-Upgrade in beide Richtungen;
- optional SSE ohne Proxy-Puffer.

Exit-Code 0 = bestanden.

Laufend überwacht:
- `GET /readyz` (intern) mit dem Check `communication`. Ist UCNG gestört oder unsicher, meldet er `degraded`, nie `down` – das Telefonbuch bleibt nutzbar.
- Metriken `tb_communication_up`, `tb_communication_secure`, `tb_communication_probe_seconds`.
- `GET /api/addons/communication/status` (angemeldet; Admins sehen Details).

## Zurückbauen
- `compose.communication.yml` weglassen bzw. `TB_COMMUNICATION_ENABLED` entfernen: Die Add-on-Routen verschwinden (404), das Telefonbuch prüft UCNG nicht mehr.
- UCNG-Daten bleiben im Volume `ucng-data` erhalten und werden nicht gelöscht (Fahrplan Kriterium 9).
