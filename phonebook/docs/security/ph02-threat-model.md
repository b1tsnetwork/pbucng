# PH-02 bis RC – Bedrohungsmodell: gemeinsame Anmeldung, E2EE-Entsperrung, Organisation und Rechte, Navigation, Betrieb

Stand: Release Candidate 1.0.0-rc.1 (27.10.2026). Bezug: ADR-005 … ADR-030. Sicherheitsprüfung des RC: [`ph11-review.md`](ph11-review.md).

## 1. Was geschützt wird

| Gut | Wo |
|---|---|
| Telefonbuch-Sitzung (Cookie `__Host-tb_session`, HttpOnly, SameSite=Strict) | Telefonbuch |
| Einmal-Code (60 s) | Telefonbuch-Speicher (nur Hash), Browser-Fragment |
| UCNG-Sitzung (Bearer, `localStorage`) | UCNG, Browser |
| Gemeinsames Geheimnis (Datei, ≥ 32 Zeichen, nicht für alle lesbar) | beide Server |
| Privater DM-Schlüssel | Browser (IndexedDB, nicht exportierbar); auf dem Server nur verschlüsselt |
| Entsperr-Geheimnis, Wiederherstellungscode | nur im Kopf bzw. beim Menschen |

## 2. Vertrauensgrenzen

```
Browser ──(Cookie)──► Telefonbuch ◄──(Bearer HMAC "ucng->phonebook")── UCNG
   │                      │──(Bearer HMAC "phonebook->ucng")──────────►│
   └──────────────(Bearer UCNG-Sitzung)────────────────────────────────┘
```

- Die beiden Schlüssel sind je Richtung abgeleitet (`HMAC-SHA256(Geheimnis, "ucng-phonebook-v1|"+Richtung)`). Ein mitgeschnittener Aufruf lässt sich nicht in die andere Richtung verwenden.
- `/api/internal/communication/*` sollte am Gateway nur aus dem internen Netz erreichbar sein (Vorlagen in `deploy/gateway`). Ohne das bleibt es durch den Schlüssel geschützt; Fehlversuche bremst die Anmeldesperre je IP.

## 3. Bedrohungen und Gegenmaßnahmen

| # | Bedrohung | Maßnahme | Test |
|---|---|---|---|
| T1 | Code wird abgefangen (Verlauf, Proxy-Log, Bildschirmfoto) | Code im Fragment (nie an einen Server), sofort aus der Adresse entfernt; 60 s; einmalig; ohne `code_verifier` wertlos (PKCE S256); falscher verifier verbraucht den Code | `TestCommSSORedeem`, `TestPhonebookExchange`, Browser |
| T2 | Login-CSRF: Angreifer schiebt dem Opfer seinen Code unter | `state` im sessionStorage des Tabs; fremder Rücksprung wird nicht eingelöst | Browser „untergeschobener Code“ |
| T3 | Code-Replay | Hash wird beim ersten Einlösen gelöscht | beide Seiten |
| T4 | Offener Redirect über `authorize` | Ziel fest = `communication.publicBaseURL`; Parameter streng geprüft | `TestCommSSOAuthorize` |
| T5 | Falsches/unsicheres UCNG bekommt Identitäten | Codes nur, wenn die Prüfung `ok` ist: Gäste aus, keine Registrierung, keine Föderation, `MODE=phonebook` mit **unserer** Adresse | `TestCommSSOAuthorize`, `TestCommunicationProbe` |
| T6 | Konto-Übernahme über gleichen Namen | Verknüpfung nur über `(provider, subject)`; LDAP = objectGUID; vorhandene Konten werden nie über den Namen übernommen | `TestPhonebookExchange` |
| T7 | Sitzung lebt nach Abmeldung/Sperre weiter | sofortiger Widerruf; Nachprüfung alle 5 min; AD-Nachprüfung 15 min; Streams und Sprachkanal werden getrennt | `TestPhonebookRevoke`, `TestCommSSOValidate`, Browser |
| T8 | Telefonbuch/Datenbank/AD fällt aus | UCNG: Sitzungen nach 15 min ohne Prüfung gesperrt (fail-closed, 503 „nicht erreichbar“ statt 401, PH-04), kommen ohne neue Anmeldung zurück; die Oberfläche behält die Sitzung. AD: letzter Stand 1 h, danach gesperrt | `TestPhonebookRevalidate`, `TestCommSSOValidate` (Datenbank weg), Browser ph02.js/ph04.js |
| T9 | Alte Passwort-Sitzungen aus `hosted` | gelten im Modus `phonebook` nicht | `TestPhonebookLegacySessions` |
| T10 | Rechteausweitung im Admin-Bereich | Rolle nur aus ausdrücklicher Zuordnung; Admin-Sitzung an Chat-Sitzung **und** Rolle gebunden; Moderatoren nur Moderation | `TestPhonebookAdminSSO`, Browser |
| T11 | Gestohlene UCNG-Sitzung liest E2EE-DMs | Server liefert nur den verschlüsselten privaten Schlüssel; Entschlüsseln braucht das Geheimnis | Browser „zweites Gerät gesperrt“ |
| T12 | Gestohlene Sitzung zerstört Schlüssel / tauscht öffentlichen Schlüssel | Änderungen nur mit `auth` des Geheimnisses bzw. Codes; öffentlicher Schlüssel unveränderlich; Zurücksetzen nur ≤ 15 min nach frischer Anmeldung, für Gegenüber sichtbar (TOFU) | `TestPhonebookE2EE` |
| T13 | Offline-Angriff auf das Geheimnis (Server-Datenleck) | PBKDF2 300 000 Runden + Mindestlänge 10; Code 130 Bit | – (Parameter) |
| T14 | Unverschlüsselte DMs, weil Schlüssel fehlen | DMs im Modus `phonebook` nur verschlüsselt, keine Uploads | `TestPhonebookE2EE`, Browser |
| T15 | DEV-Admin / Gäste des öffentlichen Telefonbuchs | kein Principal, kein Code | `TestCommSSOAuthorize` |

## 3a. PH-03: Organisation, Rechte, Medien, Austritt

| # | Bedrohung | Maßnahme | Test |
|---|---|---|---|
| T16 | Person sieht Kanal einer fremden Einheit | Gruppen nur aus dem Telefonbuch (serverseitig, Identitätsdaten), Kanalrechte `pb:org:<id>`; unbekannte Gruppen verworfen | `TestPhonebookOrgAccess`, `TestPhonebookOrgRooms`, Browser ph03.js |
| T17 | Wechsel der Einheit, aber Live-Verbindung bleibt | Gruppenänderung trennt Verbindungen zu nicht mehr lesbaren Kanälen sofort | `TestPhonebookOrgAccess` |
| T18 | Einheit gelöscht → Kanal wird öffentlich | Archiv nur für Team; entfernte Gruppen führen nie zu „alle“ | `TestPhonebookOrgRooms` |
| T19 | Leitung moderiert außerhalb ihres Bereichs | `modRoles` nur im Kanal der Einheit, nur Löschen/Anpinnen | `TestPhonebookOrgAccess` |
| T20 | Bildlink nach Rechteentzug weiter nutzbar (ADR-010) | 15–20 min, kanalgebunden, Abruf nur mit gültiger Sitzung + Leserecht, Prüfung bei jedem Abruf | `TestPhonebookPrivateMedia`, Browser ph03.js |
| T21 | Neu-Signieren eines fremden Bildes | nur für Bilder, die im Kanal vorkommen, nur mit Leserecht | `TestPhonebookPrivateMedia` |
| T22 | Ausgeschiedene Person behält Zugang/Daten | Sperre bei Meldung, Status-Abgleich aller Konten, Anonymisierung nach Frist, danach keine Wiederverknüpfung | `TestPhonebookLifecycle`, `TestCommPrincipalStatus` |
| T23 | Ausfall führt zu Massen-Sperre/Löschung | Status-Abgleich bricht bei Fehler ab, ohne zu sperren; Telefonbuch antwortet bei Datenbankfehler 503 statt „unknown“ | `TestPhonebookLifecycle` |
| T24 | Rechteausweitung über die Verwaltungsoberfläche | nur Telefonbuch-Admins (CSRF, Origin), Validierung wie die Datei, protokolliert; ungültig gespeicherter Stand wird ignoriert | `TestCommAdminPolicy` |
| T25 | Gefälschter Organisationsstand | `PUT …/org` nur mit Schlüssel „phonebook->ucng“ | `TestPhonebookOrgRooms` |

## 3b. PH-04: Einstieg und Navigation

| # | Bedrohung | Maßnahme | Test |
|---|---|---|---|
| T26 | Clickjacking: fremde Seite bettet den Chat unsichtbar ein | `frame-ancestors 'self'` + `X-Frame-Options: SAMEORIGIN` im Modus phonebook, `/admin` `DENY`; `commcheck` prüft es | `TestPhonebookFraming`, Browser ph04.js (fremde Seite blockiert, Gegenprobe) |
| T27 | Tab-Übernahme (reverse tabnabbing) zwischen Telefonbuch und Chat | alle Links zwischen beiden mit `noopener`; Telefonbuch zusätzlich COOP `same-origin` | Browser ph04.js (`window.opener === null`) |
| T28 | 503 bei Ausfall verrät oder öffnet etwas | 503 nur für Tokens bestehender, nicht widerrufener, nicht abgelaufener Sitzungen; Zugriff bleibt gesperrt (Lesen, Schreiben, Konto) | `TestPhonebookRevalidate` |
| T29 | Profil-Link verrät Personen außerhalb des Telefonbuchs | `pb` nur für angemeldete Personen, nur für Konten aus dem Telefonbuch, nicht nach Austritt; enthält nur den Anzeigenamen, der im Telefonbuch ohnehin steht | `TestPhonebookProfileLink` |

## 3c. PH-05: Visitenkarte, Zähler, Chat-Funktionen

| # | Bedrohung | Maßnahme | Test |
|---|---|---|---|
| T30 | Direktnachricht an Personen ohne Zugang oder nach Austritt | Telefonbuch prüft Nutzungsrecht beider Seiten und Sperre; UCNG löst nur aktive, nicht ausgeschiedene Konten auf, nur mit eigener Sitzung | `TestCommContact`, `TestPhonebookDMLink` |
| T31 | Principal-ID in Adresse/Verlauf | nur im Fragment (erreicht keinen Server-Log), sofort aus der Adresse entfernt; die ID ist zufällig und ohne Sitzung wertlos | Browser ph05.js |
| T32 | Zähler verrät fremde Aktivität oder Inhalte | nur für die eigene Sitzung; UCNG liefert nur zwei Zahlen, nur an das Telefonbuch (Schlüssel „phonebook->ucng“); Kanäle ohne Leserecht, blockierte Personen und Gelöschtes zählen nicht | `TestPhonebookNews`, `TestCommNews` |
| T33 | Zähler-Abfragen belasten UCNG | 30 s Zwischenspeicher je Person, Abfrage nur bei sichtbarem Fenster | `TestCommNews` |
| T34 | Spam durch Konten aus dem Telefonbuch (Link-Regel, ADR-026) | Konten sind über das AD identifiziert; Langsam-Modus, Wiederholungs-Bremse, Automoderation und Melden gelten weiter | `TestPhonebookChatParity/Spam-Bremsen` |

## 3d. PH-06 bis RC: Profilbild, Betrieb, ausgehende Verbindungen

| Nr. | Bedrohung | Maßnahme | Test |
|---|---|---|---|
| T35 | Schadhaftes Bild als Startbild (Polyglot, Metadaten, übergroß) | Telefonbuch nimmt nur PNG 128 × 128 an und kodiert neu (Textblöcke fallen weg); UCNG verarbeitet es noch einmal wie jeden Upload (`ProcessAvatar`) | `TestCommAvatar` (tEXt-Block mit gültiger Prüfsumme wird entfernt), `TestPhonebookAvatar` |
| T36 | Fremdes Startbild setzen oder abrufen | Setzen nur für das eigene Konto (Sitzung, CSRF, Origin); Abruf nur mit Schlüssel „ucng->phonebook“ (`/api/internal/*`, am Gateway gesperrt) | `TestCommAvatar` |
| T37 | Startbild überschreibt ein eigenes Bild | eigenes Bild hat Vorrang (`AvatarOwn`), Entfernen holt das aktuelle Startbild | `TestPhonebookAvatar`, Browser ph06.js |
| T38 | Admin fragt über Downloads/Webhooks das interne Netz ab (SSRF, Cloud-Metadaten) | `netguard.go`: Loopback/Link-Local/Multicast immer gesperrt, private Netze für Downloads gesperrt; Prüfung der verbundenen Adresse, auch nach Weiterleitung | `TestBlockedAddr`, `TestPhonebookOutboundGuard` |
| T39 | Daten fließen an externe Dienste (GIPHY, Tenor, Beacon) | Betriebssperren erzwungen; Telefonbuch meldet „unsicher“ ohne Sperren | `TestPhonebookMediaAndLocks`, `TestCommunicationProbe` |
| T40 | Anonymer Zugang über das Widget / fremde Herkünfte | Widget im Modus `phonebook` aus, CORS nur mit `ALLOWED_ORIGINS` | `TestPhonebookMediaAndLocks`, `TestPhonebookTemplateNoWidgets` |
| T41 | Gefälschte Client-IP (X-Forwarded-For) umgeht Sperren und Protokoll | nur die feste Gateway-Adresse gilt als Proxy (`/32`), die Adresse bleibt per `ip_range` frei | Stack-Lauf (Konfiguration), `TestConfigStackEnv` |
| T42 | Geheimnisse in Umgebung, Logs, `docker inspect` | nur als Dateien (`*_FILE`), Rechte `0440 root:65532`, UCNG prüft „nicht für alle lesbar“ | `TestEnvFile`, `TestPhonebookConfig`, Stack-Lauf |
| T43 | Ausbruch/Ausweitung aus einem Container | ohne Root, schreibgeschützt, `cap_drop: ALL`, `no-new-privileges`; nur der Gateway ist veröffentlicht | Stack-Lauf (alle Dienste gesund mit diesen Einstellungen) |
| T44 | Server-zu-Server-Schlüssel im Klartext über ein Netz | interne Adresse mit http nur für Docker-Dienstnamen (bleibt auf dem Host), sonst HTTPS (ADR-027) | `TestPhonebookConfig` |
| T45 | Gesperrtes AD-Konto bleibt im Telefonbuch angemeldet | AD-Nachprüfung alle 15 min, beendet alle Sitzungen, auch in der Kommunikation | `TestADRecheckSessions` |
| T46 | Fremder Browser nutzt einen abgesetzten Sprach-Server | Origin nur die Kommunikation, Beitritt nur mit signiertem Ticket des Chat-Servers; `/control` nur mit Agenten-Schlüssel | `TestPhonebookVoiceAgents`, Stack-Lauf (Anmeldung des Agenten) |
| T47 | Aufnahme ohne Wissen der anderen | Aufnahme nur durch Moderation, alle sehen die Anzeige; Datei nur im eigenen Browser | Browser ph06.js |

## 4. Restrisiken (bewusst)

- **Kompromittierter UCNG-Server** kann verschlüsselte Schlüsselblöcke ausliefern und – wie heute in `hosted` – manipulierten JavaScript-Code. E2EE schützt gegen Datenlecks und neugierige Betreiber, nicht gegen einen aktiv manipulierten Server (unverändert gegenüber UCNG).
- **Browser-Kompromittierung** (Malware, Erweiterungen) ist nicht abgedeckt.
- **Gleiche Person auf einem geteilten Rechner**: der entsperrte Schlüssel bleibt in diesem Browserprofil (wie bisher). Abmelden im Chat löscht ihn.
- **Codes im Speicher** des Telefonbuchs: mehrere Telefonbuch-Instanzen hinter einem Lastverteiler würden Codes nicht teilen (wie heute die Anmeldesperre). Betrieb: eine Instanz.
- **Widerruf-Warteschlange** liegt im Speicher; Verlust bei Neustart, dann greift die Nachprüfung (≤ 5 min).
- **LDAP ohne GUID-Attribut**: Verknüpfung über den Benutzernamen (Warnung beim Start).
- **Team-Abzeichen** liest alle Kanäle mit Rollen, auch Organisationskanäle – wird deshalb nicht aus dem Telefonbuch vergeben.
- **Admin-Bereich-Bildlinks** sind nicht an eine Sitzung gebunden (15–20 min gültig).
- **Visitenkarte verrät, ob jemand die Kommunikation nutzt:** Angemeldete Kolleginnen und Kollegen sehen beim Klick auf „Nachricht“, ob die Person sich schon angemeldet hat bzw. Zugang hat. Gäste nicht.
- **Austritt ohne Sitzung** wird spätestens nach `PHONEBOOK_LIFECYCLE` (6 h) erkannt; mit Sitzung sofort bzw. ≤ 5 min.
- **AD-Störung:** Die Nachprüfung im Telefonbuch selbst ist fail-open; ein im AD gesperrtes Konto bleibt angemeldet, solange das AD nicht erreichbar ist (die Kommunikation sperrt nach `adGrace`).
- **Startbild nach Admin-Änderung:** Ändert ein Admin das Avatar einer anderen Person, kommt es erst in der Kommunikation an, wenn diese Person das Telefonbuch öffnet.
- **Webhooks dürfen private Netze erreichen** (Bots laufen intern). Loopback und Link-Local bleiben gesperrt.
- **Aufnahmen** liegen danach auf dem Gerät der aufnehmenden Person – außerhalb jeder Löschfrist.
