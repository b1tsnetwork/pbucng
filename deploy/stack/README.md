# Telefonbuch mit Kommunikation – Installation (Release Candidate 1.0.0-rc.1)

Diese Referenz-Installation enthält alle Komponenten für einen Server mit Docker Compose. Abgesetzte Sprach-Server stehen in `voice-node/`.

```
                Browser (Beschäftigte)
                   │ 443                         │ 443 (WebSocket /ws)
┌──────────────────▼───────────────────┐   ┌──────▼──────────────────────┐
│ gateway (Caddy, einzig erreichbar)   │   │ voice-node: gateway (Caddy) │
│  telefonbuch.firma.local → app:8080  │   │  /ws /control /healthz      │
│  kommunikation.firma.local → ucng    │   │        → ucng-voice:8090    │
└───────┬──────────────────┬───────────┘   └──────▲──────────────────────┘
        │ edge             │                      │ https://kommunikation…/api/voice/…
┌───────▼───────┐  ┌───────▼────────┐             │ (Anmeldung des Agenten)
│ app           │◄─┤ ucng           │─────────────┘
│ (Telefonbuch) │─►│ (Kommunikation)│  Server-zu-Server: http://app:8080 / http://ucng:8080
└───────┬───────┘  └────────────────┘  (Schlüssel je Richtung, nur im Docker-Netz)
        │ backend
┌───────▼───────┐  ┌─────────────┐  ┌──────────────────┐
│ db (MySQL 8.4)│◄─┤ db-backup   │  │ stt (aus, ADR-011)│
└───────────────┘  └─────────────┘  └──────────────────┘
        LDAPS 636 → Domänencontroller
```

| Datei | Zweck |
|---|---|
| `compose.yml` | alle Dienste, gehärtet |
| `compose.build.yml` | optional: Images aus dem Quellcode dieses Repositorys bauen |
| `.env.example` | Einstellungen (Adressen, Versionen, TLS) – nach `.env` kopieren |
| `config/telefonbuch.json` | Telefonbuch: LDAP/AD, Datenhaltung, Kommunikation |
| `config/ca/` | Stammzertifikat der Firma (LDAPS) |
| `gateway/Caddyfile`, `gateway/certs/` | Gateway und Zertifikate |
| `secrets/create.sh` | erzeugt alle Geheimnisse |
| `backup/mysql-backup.sh` | tägliche Datenbank-Sicherung |
| `check.sh` | Prüfung durch den Gateway (`telefonbuch commcheck`) |
| `voice-node/` | abgesetzter Sprach-Server (je Server einmal) |

Entscheidungen und Begründungen: [`docs/RELEASE-ENTSCHEIDUNGEN.md`](../../phonebook/docs/RELEASE-ENTSCHEIDUNGEN.md). Sicherheitsprüfung: [`docs/security/ph11-review.md`](../../phonebook/docs/security/ph11-review.md).

> **Vor dem Produktivbetrieb:** Die Lizenz- und Markenprüfung (ADR-016) fehlt noch. Dieser Release Candidate ist für Abnahme und Test bestimmt.

## 1. Voraussetzungen

- Linux-Server mit Docker Engine ≥ 24 und Docker Compose ≥ 2.24.
- Zwei DNS-Namen auf den Server, z. B. `telefonbuch.firma.local` und `kommunikation.firma.local`. Es müssen zwei Namen sein, kein Pfad (ADR-004).
- Zertifikate für beide Namen (Firmen-CA) oder `TLS_MODE=acme` bei öffentlich auflösbaren Namen.
- LDAPS-Zugang zum AD: Dienstkonto mit Lesezugriff und Stammzertifikat der Firma.
- Zugriff auf die Images in `ghcr.io/b1tsnetwork/…` oder eine eigene Registry (`TB_IMAGE`, `UCNG_IMAGE`).

**Firewall**

| Richtung | Port | Wozu |
|---|---|---|
| Clients → Server | 443/tcp (80/tcp für die Umleitung) | Telefonbuch, Kommunikation |
| Server → Domänencontroller | 636/tcp | LDAPS |
| Clients → Sprach-Server | 443/tcp | Sprache/Video (WebSocket, kein UDP) |
| Sprach-Server → Server | 443/tcp | Anmeldung des Agenten |
| Server → Sprach-Server | 443/tcp | Steuerung (`/control`) |

## 2. Installation

```sh
cd deploy/stack
cp .env.example .env
$EDITOR .env                       # TB_DOMAIN, COMM_DOMAIN, UCNG_VERSION, TLS_MODE …
sudo sh secrets/create.sh          # fragt nach dem Passwort des LDAP-Dienstkontos
$EDITOR config/telefonbuch.json    # ldap.url, bindDN, baseDN, adminGroupDN, userGroupDN
cp /pfad/firma-root-ca.pem config/ca/firma-root-ca.pem
cp tb.crt tb.key comm.crt comm.key gateway/certs/   # bei TLS_MODE=files; chmod 600 gateway/certs/*.key
docker compose pull
docker compose up -d
sh check.sh
```

**Images selbst bauen** (statt sie aus der Registry zu holen):
- `docker compose -f compose.yml -f compose.build.yml build` baut Telefonbuch und Kommunikation aus `../../phonebook` und `../../ucng`.
- `voice-node/compose.build.yml` baut den Sprach-Server.
- Für alle Images, mehrere Plattformen und den Upload in eine Registry: `../../build-images.sh --push`.

**Erste Anmeldung und Einrichtung**

1. Im Telefonbuch mit einem Konto aus `adminGroupDN` anmelden.
2. Unter *Verwaltung → Kommunikation verwalten* festlegen:
   - wer die Kommunikation nutzen darf,
   - Admin- und Moderatorzuordnung,
   - Organisationskanäle.
3. Den Notfallzugang zum Admin-Bereich der Kommunikation (`/admin`) gibt es mit dem Passwort aus `secrets/ucng_admin_password`. Im Alltag melden sich Admins über das Telefonbuch an.

**Was `check.sh` prüft** (Ergebnis 0 Fehler, sonst Exit-Code 1):
- getrennte Herkunft beider Domains;
- Erreichbarkeit;
- Sicherheitsprofil der Kommunikation: kein Gastzugang, keine Registrierung, keine Föderation, Betriebssperren;
- keine Einbettung in fremde Seiten;
- SSE ohne Proxy-Puffer und WebSocket-Weiterleitung.

## 3. Einstellungen (`.env`)

| Variable | Standard | Bedeutung |
|---|---|---|
| `COMPOSE_PROFILES` | `communication,backup` | Dienste-Gruppen. Ohne `communication` läuft das Telefonbuch allein (dann `COMMUNICATION_ENABLED=false`). `stt` erst nach Freigabe. |
| `TB_VERSION`, `UCNG_VERSION` | `1.0.0-rc.1` | **feste** Versionen, nie `latest` |
| `TB_DOMAIN`, `COMM_DOMAIN` | – | die beiden Namen |
| `TLS_MODE` | `files` | `files` (gateway/certs), `internal` (nur Test), `acme` |
| `EDGE_SUBNET`, `GATEWAY_IP`, `EDGE_IP_RANGE` | `172.30.10.0/24`, `.2`, `.128/25` | Docker-Netz zwischen Gateway und Diensten; nur der Gateway darf `X-Forwarded-For` setzen. Nur ändern, wenn sich das Netz mit dem Firmennetz überschneidet. |
| `UCNG_PHONEBOOK_REVALIDATE` / `_GRACE` | `5m` / `15m` | Nachprüfung der Chat-Sitzungen (ADR-013) |
| `UCNG_PHONEBOOK_LIFECYCLE` / `_RETENTION_DAYS` | `6h` / `90` | Austritt erkennen bzw. anonymisieren (ADR-022) |
| `UCNG_VOICE_BUILTIN` | `1` | `0` = nur abgesetzte Sprach-Server |
| `UCNG_TRANSCRIPT_KEEP_DAYS` | `14` | Frist für Transkripte (höchstens 365, ADR-011) |
| `UCNG_BACKUP_KEEP`, `DB_BACKUP_KEEP_DAYS`, `DB_BACKUP_TIME` | `14`, `14`, `02:30` | Sicherungen |

Geheimnisse stehen nie in `.env`, sondern in `secrets/`. Die Dateien haben `root:65532` und Modus `0440`; die Container lesen sie über die Gruppe 65532.

## 4. Abgesetzte Sprach-Server (`voice-node/`)

Für viele gleichzeitige Gespräche, Bildschirmfreigaben oder Standorte. Je Server:

```sh
cd voice-node
cp .env.example .env && $EDITOR .env     # AGENT_ID, VOICE_DOMAIN, COMM_DOMAIN, UCNG_VERSION
sudo install -o root -g 65532 -m 0440 /sicher/kopiert/voice_agent_token secrets/voice_agent_token
cp voice.crt voice.key certs/            # bei TLS_MODE=files
docker compose up -d
```

- Der Server meldet sich bei der Kommunikation an. Im Admin-Bereich unter *Sprach-Server* erscheint er mit seinem Namen.
- Neue Sprachräume verteilt der Chat-Server auf die angemeldeten Server.
- Mit `UCNG_VOICE_BUILTIN=0` am Haupt-Server laufen alle Gespräche über die abgesetzten Server.

## 5. Betrieb

### Aktualisieren
```sh
docker compose run --rm db-backup now          # Sicherung vorher
$EDITOR .env                                   # TB_VERSION / UCNG_VERSION
docker compose pull && docker compose up -d
sh check.sh
```
Das Telefonbuch führt Datenbank-Migrationen beim Start selbst aus. Die Migrationen gehen nur vorwärts.

### Zurück auf die vorherige Version
1. Alte Versionen in `.env` eintragen.
2. Wenn Migrationen gelaufen sind, die Datenbank aus der Sicherung von vorher zurückspielen (siehe unten).
3. `docker compose up -d`.

### Sicherung
- **Telefonbuch-Datenbank:** täglich nach `backup/dumps/telefonbuch-JJJJ-MM-TT_HHMM.sql.gz` (Profil `backup`). Sofort: `docker compose run --rm db-backup now`.
- **Kommunikation:** täglich um 3 Uhr im Volume `ucng-data` unter `/data/backups`. Herunterladen kann man die Sicherungen auch im Admin-Bereich.
- Beide Ordner gehören in die Sicherung des Servers. Sie enthalten personenbezogene Daten; verschlüsselt ablegen.
- Die JSON-Kopie des Telefonbuchs (`tb-data`) ist die Rückfallebene bei einem Datenbankausfall, keine Sicherung.

### Wiederherstellen
```sh
# Telefonbuch-Datenbank
gunzip -c backup/dumps/telefonbuch-2026-10-27_0230.sql.gz | \
  docker compose exec -T db sh -c 'mysql -u root -p"$(cat /run/secrets/db_root_password)" telefonbuch'

# Kommunikation (Archiv aus /data/backups oder aus dem Admin-Bereich)
docker compose stop ucng
docker compose run --rm --no-deps -v "$PWD/ucng-sicherung.tar.gz:/restore.tar.gz:ro" --entrypoint sh ucng -c \
  'cd /data && find . -mindepth 1 -maxdepth 1 ! -name backups -exec rm -rf {} + && tar -xzf /restore.tar.gz'
docker compose up -d ucng
```

Nach dem Zurückspielen gehören die Personen aus dem Telefonbuch wieder zu ihren bisherigen Konten (getestet: `TestPhonebookBackupRestore`). Verschlüsselte Direktnachrichten lassen sich nur mit dem Entsperr-Geheimnis der jeweiligen Person lesen.

### Überwachung
| Ziel | Adresse |
|---|---|
| Telefonbuch lebt / ist bereit | `https://TB_DOMAIN/livez`, `/readyz` (Datenhaltung, AD-Abgleich, Kommunikation) |
| Telefonbuch-Metriken (Prometheus) | `https://TB_DOMAIN/metrics` mit `Authorization: Bearer <secrets/metrics_token>`; u. a. `tb_communication_up`, `tb_communication_secure` |
| Kommunikation | `https://COMM_DOMAIN/healthz` |
| Container | `docker compose ps` (alle Dienste mit Gesundheitsprüfung) |
| Protokolle | `docker compose logs -f app ucng gateway` (JSON-Datei, je Dienst 5 × 10 MB) |

`tb_communication_secure = 0` heißt: Die Kommunikation läuft ohne das Sicherheitsprofil, zum Beispiel ohne Betriebssperren oder mit zu langer Transkript-Frist. Die Gründe stehen unter *Verwaltung → Kommunikation verwalten*.

### Geheimnisse wechseln
`metrics_token`, `ucng_integration_secret`, `ucng_admin_password`, `voice_agent_token`, `ldap_bind_password`:
```sh
sudo rm secrets/<name> && sudo sh secrets/create.sh     # ldap_bind_password: fragt nach dem neuen Passwort
docker compose up -d --force-recreate
```
- `voice_agent_token`: die neue Datei auf alle Sprach-Server verteilen und dort ebenfalls neu starten.
- Bestehende Chat-Sitzungen bleiben gültig; die Server-zu-Server-Schlüssel gelten sofort neu.

`db_password` und `db_root_password` übernimmt MySQL nur beim allerersten Start. Zum Wechseln zuerst in der Datenbank ändern, dann die Datei:
```sh
docker compose exec db sh -c 'mysql -u root -p"$(cat /run/secrets/db_root_password)" -e "ALTER USER '"'"'telefonbuch'"'"'@'"'"'%'"'"' IDENTIFIED BY '"'"'NEUES-PASSWORT'"'"'"'
printf %s 'NEUES-PASSWORT' | sudo tee secrets/db_password >/dev/null   # Rechte bleiben (root:65532, 0440)
docker compose up -d --force-recreate app
```

## 6. Fehlerbehebung

| Meldung | Ursache |
|---|---|
| `ldap.caFile: … no such file` | `config/ca/firma-root-ca.pem` fehlt |
| UCNG: `PHONEBOOK_SECRET_FILE ist für alle lesbar` | Rechte von `secrets/` – `sh secrets/create.sh` setzt sie |
| UCNG: `PHONEBOOK_INTERNAL_URL: HTTPS ist Pflicht` | interne Adresse mit Punkt oder IP: Dienstnamen wie `http://app:8080` verwenden oder HTTPS (ADR-027) |
| Gateway startet nicht: `Address already in use` | `GATEWAY_IP` liegt in `EDGE_IP_RANGE` oder ist belegt |
| `check.sh`: SSE verspätet | ein zusätzlicher Proxy vor dem Gateway puffert; dort Puffern für die Kommunikations-Domain abschalten |
| Telefonbuch: Kommunikation „unsicher“ | `UCNG_VERSION` ist kein Stand mit `MODE=phonebook`, oder `UCNG_STT_URL` ist ohne Freigabe gesetzt |

## 7. Was hier getestet ist und was nicht

- **Getestet:** Der Stack lief vollständig mit allen Härtungen (schreibgeschützt, ohne Root, ohne Capabilities, Geheimnisse als Dateien), mit und ohne Kommunikation. Ein abgesetzter Sprach-Server meldete sich über den Gateway an. `commcheck` durch den Gateway ergab 17/17.
- **Bedingungen des Tests:** Die Registries waren gesperrt. Deshalb liefen statt der Release-Images Platzhalter-Images aus denselben Programmen und ein MySQL-kompatibler Ersatz. Statt Caddy lief ein Test-Gateway (http).
- **Nicht ausgeführt:** echte Images, Caddy und TLS. Das prüft der Workflow `.github/workflows/stack.yml` mit echten Images, `TLS_MODE=internal`, `check.sh`, Datenbank-Sicherung und Sprach-Server. Er muss vor der Abnahme einmal erfolgreich gelaufen sein.
- **Ebenfalls nicht in Containern ausgeführt:** die Befehle zum Wiederherstellen (§5). Die Wiederherstellung der Kommunikation ist per Unit-Test geprüft.
