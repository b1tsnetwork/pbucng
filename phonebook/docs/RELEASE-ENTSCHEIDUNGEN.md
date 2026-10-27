# Release Candidate 1.0.0-rc.1 – Entscheidungen

**Stand:** 27.10.2026 · gilt für Telefonbuch `v1.0.0-rc.1` und UCNG (Branch `phonebook-integration`) `v1.0.0-rc.1`

Dieses Dokument trennt drei Arten von Entscheidungen:
- **Teil A:** Was der Projektinhaber entschieden hat.
- **Teil B:** Was ich für den Release Candidate selbst entschieden und umgesetzt habe (Empfehlungen, Auftrag vom 27.10.2026). Jede Entscheidung lässt sich zurücknehmen; wie, steht jeweils dabei.
- **Teil C:** Was noch offen ist und nicht von mir entschieden werden darf.

Die ausführliche Begründung der Architekturentscheidungen steht in [`integration-decisions.md`](integration-decisions.md) (ADR-001 … ADR-030).

---

## Teil A – Entscheidungen des Projektinhabers

| Thema | Entscheidung | Datum | ADR |
|---|---|---|---|
| Betrieb der Kommunikation | eigene Subdomain statt Pfad-Präfix | 26.10.2026 | ADR-004 |
| Verschlüsselte Direktnachrichten | eigenes Entsperr-Geheimnis mit Wiederherstellungscode, kein zweites Passwort | 26.10.2026 | ADR-007 |
| Admin- und Moderatorrechte | ausdrückliche Zuordnung; Telefonbuch-Admins nur zur Inbetriebnahme automatisch | 26.10.2026 | ADR-008 |
| Private Medien nach Rechteentzug | kurz gültige, an die Sitzung gebundene Links | 26.10.2026 | ADR-010 |
| Rechte während einer Sitzung | regelmäßige Nachprüfung; AD-Nachprüfung auch für das Telefonbuch selbst (eigene Phase) | 26.10.2026 | ADR-013 |
| Sprechzeiten und Öffnungszeiten | getrennt | 26.10.2026 | ADR-014 |
| Organisationskanäle, Austritt | Kanäle je Einheit; Austritt sperrt sofort, anonymisiert nach 90 Tagen | 26.10.2026 | ADR-021, ADR-022 |
| Zähler am Knopf „Kommunikation“ | „neu seit letztem Besuch“ | 27.10.2026 | ADR-025 |
| Profilbild | das Pixel-Avatar aus dem Telefonbuch ist das Startbild in der Kommunikation und lässt sich dort ersetzen | 27.10.2026 | ADR-015 |
| Umfang | Release Candidate mit allen Komponenten, empfohlene Maßnahmen umsetzen | 27.10.2026 | – |

---

## Teil B – Meine Entscheidungen für den Release Candidate

### B-1 Ablauf des Startbilds (Umsetzung von ADR-015)

| | |
|---|---|
| **Entscheidung** | Der Browser rendert das Pixel-Avatar als PNG (128 × 128). Er überträgt es an das Telefonbuch, sobald die Person das Telefonbuch öffnet und sich das Avatar geändert hat. Das Telefonbuch prüft und kodiert das Bild neu; Metadaten werden entfernt. UCNG holt es mit dem Schlüssel „ucng->phonebook“: bei der Anmeldung und bei jeder Nachprüfung (Standard alle 5 min), aber nur, wenn sich der Fingerabdruck geändert hat. |
| **Eigenes Bild** | Ein in der Kommunikation hochgeladenes Bild hat Vorrang; Änderungen im Telefonbuch überschreiben es nicht. Wird es entfernt, ist sofort wieder das aktuelle Telefonbuch-Avatar da. |
| **Standard-Avatar** | Wird ebenfalls übertragen, damit alle ein Bild haben. |
| **Begründung** | Das 3D-Pixel-Avatar wird nur im Browser gerendert; ein zweiter Renderer im Server wäre eine zweite Quelle für Abweichungen. Ein Fingerabdruck verhindert unnötige Übertragungen. |
| **Grenze** | Ändert ein Admin das Avatar einer Person, kommt die Änderung erst an, wenn diese Person das Telefonbuch wieder öffnet. |
| **Zurücknehmen** | Ohne Kommunikation entfällt alles. Ein Schalter je Person ist nicht vorgesehen; die Person ersetzt das Bild einfach in der Kommunikation. |
| **Geprüft** | `TestCommAvatar`, `TestPhonebookAvatar`; Browser `ph06.js`: 12 Prüfungen, darunter pixelgleich, Änderung nach 15 s, Ersetzen, Bestand bei Änderung, Zurücksetzen, Sicht anderer |

### B-2 Betriebssperren Beacon und externe GIFs (ADR-009 abgeschlossen)

| | |
|---|---|
| **Entscheidung** | Im Modus `phonebook` sind `LOCK_BEACON` und `LOCK_EXTERNAL_GIFS` immer gesetzt. Damit gibt es keine Verbindungen zu GIPHY, Tenor oder einem Beacon und keine Sprachpakete aus dem Beacon. Die eigene GIF-Bibliothek und Emotes bleiben. |
| **Begründung** | Externe Dienste würden Suchbegriffe und IP-Adressen der Beschäftigten nach außen geben. |
| **Telefonbuch** | Meldet die Kommunikation als „unsicher“, wenn die Sperren fehlen. |
| **Zurücknehmen** | Nur per Code-Änderung in UCNG (`oplocks.go`), weil der Modus `phonebook` alle Sperren erzwingt. `communication.features.beacon` im Telefonbuch würde nur die Warnung abstellen. |

### B-3 Kein Widget für fremde Webseiten im Modus `phonebook`

| | |
|---|---|
| **Entscheidung** | Widgets lassen sich nicht anlegen (403). Vorlagen mit Widgets werden abgelehnt. Fremde Herkünfte (CORS) gibt es nur mit ausdrücklichem `ALLOWED_ORIGINS`. Der Admin-Bereich blendet Widgets, Beacon und Kanal-Abos aus. |
| **Begründung** | Das Widget ist für anonyme Gäste gebaut; im Modus `phonebook` gibt es keinen Gastzugang (Gate A). |
| **Zurücknehmen** | Nur per Code-Änderung (bewusst). |

### B-4 Schutz ausgehender Verbindungen (SSRF)

| | |
|---|---|
| **Entscheidung** | Im Modus `phonebook` laufen Downloads aus dem Admin-Bereich (Emotes/GIFs per Adresse) und Bot-Webhooks über einen geprüften Client. Immer gesperrt: Loopback, Link-Local (u. a. Cloud-Metadaten 169.254.169.254), Multicast. Private Netze: für Downloads nur mit `OUTBOUND_ALLOW_PRIVATE=1`, für Webhooks erlaubt, weil Bots meist intern laufen. |
| **Begründung** | Admin-Rechte bekommen im Modus `phonebook` auch Personen, die das Telefonbuch zuordnet. Darüber soll niemand Datenbank, Telefonbuch-Schnittstellen oder Metadaten-Dienste abfragen können. |
| **Prüfung** | Die tatsächlich verbundene Adresse wird nach der Namensauflösung geprüft (hilft gegen DNS-Rebinding), auch bei Weiterleitungen und über einen Proxy. |
| **Geprüft** | `TestBlockedAddr`, `TestPhonebookOutboundGuard` |

### B-5 Aufbewahrung von Transkripten (Umsetzung von ADR-011)

| | |
|---|---|
| **Entscheidung** | Im Modus `phonebook` gilt ohne Angabe eine Frist von 14 Tagen statt „unbegrenzt“. Mehr als 365 Tage verhindern den Start. |
| **Spracherkennung** | Bleibt aus, bis `communication.features.transcription` gesetzt ist. Ist `STT_URL` ohne diesen Schalter gesetzt, meldet das Telefonbuch „unsicher“. |
| **Rechtliche Freigabe** | Fehlt noch (Teil C). Der STT-Dienst ist im Stack ein eigenes Profil `stt`, das standardmäßig aus ist. |

### B-6 AD-Nachprüfung für das Telefonbuch selbst (ADR-013, „eigene Phase“)

| | |
|---|---|
| **Entscheidung** | Für AD-Sitzungen prüft das Telefonbuch höchstens alle 15 min (`auth.adRecheck`, 1 min bis 24 h) im Hintergrund das AD-Konto. Eine Sperre, Löschung oder fehlende Anmeldegruppe beendet alle Sitzungen der Person, auch in der Kommunikation. Eine geänderte Admin-Berechtigung beendet die Telefonbuch-Sitzungen; die Person meldet sich mit den neuen Rechten neu an. |
| **Bei AD-Störung** | **fail-open**: Die Sitzungen bleiben bestehen. |
| **Begründung** | Eine AD-Störung soll das Telefonbuch (Notfall-Nachschlagewerk) nicht für alle unbenutzbar machen. Für die Kommunikation bleibt es bei fail-closed nach `communication.adGrace`. |
| **Zurücknehmen** | Ein Ausschalten ist nicht vorgesehen. Mit `auth.adRecheck = "24h"` wird praktisch nur noch bei der Anmeldung geprüft. |
| **Geprüft** | `TestADRecheckSessions` (frisch angemeldet, fällig und in Ordnung, AD-Fehler, gesperrt, gelöscht, ohne Anmeldegruppe, Admin geändert, lokale Konten unberührt) |

### B-7 Interne Adressen: http nur für Docker-Dienstnamen (neu, ADR-027)

| | |
|---|---|
| **Befund** | Beim Start des Stacks gefunden: UCNG lehnte `PHONEBOOK_INTERNAL_URL=http://app:8080` ab, die bisherige Anleitung (`compose.communication.yml`) hätte nicht gestartet. |
| **Entscheidung** | Die interne Adresse darf `http` nutzen, wenn der Host ein Name ohne Punkt ist, also ein Dienstname im selben Docker-Netz. Der Verkehr verlässt dann den Host nicht. IP-Adressen und Namen mit Punkt brauchen HTTPS. Die öffentliche Adresse (`PHONEBOOK_URL`) braucht immer HTTPS, außer bei `localhost`. |
| **Begründung** | Die Schlüssel je Richtung gehen als Kopfzeile mit. Über ein echtes Netz gehört das in TLS, im Docker-Bridge-Netz eines Hosts nicht. |
| **Geprüft** | `TestPhonebookConfig` (5 neue Fälle, Mutationstest); Stack-Lauf |

### B-8 Referenz-Installation `deploy/stack` (Compose)

| Punkt | Entscheidung |
|---|---|
| Erreichbarkeit | Von außen nur der Gateway (80/443). Datenbank, Telefonbuch und UCNG haben keine veröffentlichten Ports. |
| Container | Ohne Root, Dateisystem schreibgeschützt (außer MySQL), `no-new-privileges`, alle Capabilities entzogen. Der Gateway hat nur `NET_BIND_SERVICE`. MySQL läuft direkt als Benutzer 999. |
| Geheimnisse | Nur als Dateien (Compose-Secrets), nie in `.env`. Rechte `root:65532`, Modus `0440`. UCNG und MySQL lesen über die Gruppe 65532 (`group_add`). Neu: `<NAME>_FILE` für alle UCNG-Geheimnisse. |
| Proxy-Vertrauen | Nur die feste Adresse des Gateways darf `X-Forwarded-For` setzen (`/32`), nicht das ganze Netz. Damit die Adresse frei bleibt, bekommen andere Container Adressen nur aus dem oberen Bereich (`ip_range`). Gefunden beim Stack-Lauf: Ohne `ip_range` konnte ein anderer Container die Gateway-Adresse belegen. |
| TLS | Drei Arten: `files` (Firmen-CA, Standard), `internal` (Caddy-CA, nur Test), `acme`. |
| Gateway | Sperrt `/api/internal/*` und die UCNG-Server-zu-Server-Pfade nach außen. SSE ohne Puffer, WebSocket ohne Zeitlimit. |
| Dienste-Gruppen | `communication` und `backup` sind Standard. `stt` ist aus (B-5). Ohne `communication` läuft das Telefonbuch allein (geprüft). |
| Sicherung | Täglich `mysqldump`, 14 Tage aufbewahrt. UCNG sichert seine Daten selbst, die letzten 14. |
| Prüfung | `check.sh` ruft `telefonbuch commcheck` durch den Gateway auf, mit der richtigen CA. |
| Gesundheitsprüfung | `ucng healthcheck` und `ucng-voice healthcheck` statt `wget`, damit sie in Minimal-Images läuft. |

### B-9 Abgesetzte Sprach-Server (`deploy/stack/voice-node`)

| Punkt | Entscheidung |
|---|---|
| Aufbau | Eine Installation je Server: `ucng-voice` plus Caddy. |
| Erreichbarkeit | Von außen nur `/ws` (Browser), `/control` (nur mit Agenten-Schlüssel) und `/healthz`, sonst 404. |
| Erlaubte Herkunft | Browser dürfen nur von der Kommunikations-Domain verbinden (`ALLOWED_ORIGINS`). |
| Anmeldung | Der Agent meldet sich über `https://COMM_DOMAIN` an. Der Schlüssel liegt als Datei vor (`AGENT_TOKEN_FILE`). |

### B-10 Versionen und Veröffentlichung

| Punkt | Entscheidung |
|---|---|
| Version | Beide Repositories `v1.0.0-rc.1`; das macht die Zuordnung eindeutig. |
| Vorabversionen | Tags mit Bindestrich erzeugen eine Vorabversion (GitHub Release als „Pre-release“). Sie bekommen kein `latest`, `1.0` oder `1`. |
| Workflows | `release.yml`: Images, Programmdateien, Installationspaket des Stacks, Windows-Symbol, Prüfsummen. `stack.yml`: Compose- und Caddy-Prüfung, Start mit echtem MySQL und Caddy, `check.sh`, Sicherung, Sprach-Server. Seit B-10a gemeinsam für beide Teile. |
| Schwachstellen | `govulncheck` läuft jetzt auch im Telefonbuch-CI (UCNG hatte es schon). |
| `.gitignore` | Befund: `secrets/`, `config.json` und `telefonbuch.json*` passten auf jeden Unterordner. Damit wären `deploy/stack/secrets/create.sh` und `deploy/stack/config/telefonbuch.json` nicht eingecheckt worden. Die Muster gelten jetzt nur im Wurzelordner. |

### B-10a Ein Repository für alles (27.10.2026, auf Wunsch)

| | |
|---|---|
| **Aufbau** | `phonebook/` (Telefonbuch), `ucng/` (Kommunikation, AGPL), `deploy/stack/` (Installation). Die gemeinsamen Workflows stehen in `.github/` im Wurzelordner. Jeder Teil behält sein eigenes `go.mod`, seine Dockerfiles und seine Lizenzdateien. |
| **Images** | Eigene Pakete im Namensraum des Repositorys, getrennt von den früheren Repos: `ghcr.io/b1tsnetwork/pbucng/phonebook`, `…/ucng`, `…/ucng-voice`, `…/ucng-beacon`, `…/ucng-bot-music`. Die Pakete gehören zu `pbucng` (Label `org.opencontainers.image.source`); `deploy/stack` verweist auf diese Namen. |
| **Workflows** | `ci.yml` prüft beide Teile. `release.yml` ist ein Release mit allen Images und Programmdateien. `stack.yml` baut alle Images aus dem Stand statt sie zu holen. `cla.yml` bezieht sich auf `ucng/CLA.md`. |
| **Lokal bauen** | `build-images.sh` (alle Images, optional `--push`). `deploy/stack/compose.build.yml` baut Telefonbuch und Kommunikation direkt aus dem Quellcode. |
| **Quellcode-Hinweis (AGPL)** | Images und Programme zeigen auf `…/tree/<commit>/ucng`. |
| **Git-Verlauf** | Nicht übernommen: Das Repository beginnt mit dem Stand `1.0.0-rc.1`. Die Patch-Folge PH-00 … RC liegt bei den früheren Lieferungen. |

### B-11 Team (Moderation in Sprachkanälen) bleibt in UCNG

| | |
|---|---|
| **Entscheidung** | Unverändert seit PH-03: Das Team-Abzeichen wird im UCNG-Admin-Bereich vergeben, nicht aus dem Telefonbuch abgeleitet. |
| **Begründung** | Das Team liest alle Kanäle mit Rollen, auch Organisationskanäle. Im Browser-Test (`ph06.js`) wird es deshalb ausdrücklich vergeben, bevor moderiert und aufgenommen wird. |

### B-12 Nachprüfung bei Ausfall: auch `…/phonebook/user`

| | |
|---|---|
| **Entscheidung** | Ist das Telefonbuch nicht erreichbar, antwortet UCNG auch auf `GET /api/integration/phonebook/user` mit 503, wie bei den übrigen Chat-Endpunkten (ADR-024). |
| **Begründung** | Einheitliches Verhalten; der Endpunkt verrät sonst den Stand einer nicht mehr geprüften Sitzung. |

---

## Teil C – Offen (nicht von mir zu entscheiden)

| Punkt | Warum offen | Folge |
|---|---|---|
| **ADR-016 Lizenz und Marke** | Rechtsprüfung. AGPL §13: Die geänderte UCNG-Fassung muss ihren Nutzerinnen und Nutzern (auch Beschäftigten) den Quellcode anbieten. Die Release-Images tragen `SOURCE_URL` (Repository und Commit), das Repository muss für sie erreichbar sein. Dazu Name und Logo „UCNG“ (`TRADEMARKS.md`). | **Kein Produktivbetrieb vor der Prüfung (Gate E).** Der RC ist für Abnahme und Test. |
| **ADR-011 Transkription** | Information und Einwilligung (Personalvertretung, Datenschutz) | STT bleibt aus (B-5) |
| **Abnahme mit echtem AD** | Hier nur mit nachgebildetem LDAP getestet | Abnahme vor Ort: Anmeldung, Gruppen, Sperre → Abmeldung (B-6) |
| **Abnahme mit echten Images, Caddy, Browsern** | Hier waren die Registries gesperrt (siehe Release Notes) | `stack.yml` in GitHub laufen lassen; Abnahme mit Firefox/Edge/Safari |
| **Aufbewahrungsfristen** | Nachrichten 90 Tage nach Austritt, Transkripte 14 Tage, Sicherungen 14 Tage | Mit Datenschutz abstimmen; alle Werte sind einstellbar |
