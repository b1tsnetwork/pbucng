# Telefonbuch + Kommunikation

Internes Telefonbuch (Go, LDAP/AD, MySQL/JSON, Pixel-Avatare) mit optionaler Kommunikation auf Basis von **UCNG**. Die Kommunikation bietet Chat, verschlüsselte Direktnachrichten, Sprach- und Videokanäle und abgesetzte Sprach-Server. Eine Anmeldung, ein Verzeichnis, zwei Subdomains.

**Version:** `1.0.0-rc.1` (Release Candidate – siehe [Release Notes](phonebook/RELEASE-NOTES.md)). Vor dem Produktivbetrieb fehlt noch die Lizenz- und Markenprüfung (ADR-016).

## Aufbau

| Ordner | Inhalt | Sprache/Build | Lizenz |
|---|---|---|---|
| [`phonebook/`](phonebook/README.md) | Telefonbuch (Server, Weboberfläche, Windows-Symbol) | Go 1.24, eigenes `go.mod`, `Dockerfile` | intern (keine Lizenzdatei) |
| [`ucng/`](ucng/README.md) | Kommunikation UCNG mit Modus `phonebook`, Sprach-Server, Beacon, Musik-Bot | Go 1.27, eigenes `go.mod`, `Dockerfile*` | AGPL-3.0-or-later ([`ucng/LICENSE`](ucng/LICENSE), [`NOTICE`](ucng/NOTICE), [`TRADEMARKS.md`](ucng/TRADEMARKS.md), [`CLA.md`](ucng/CLA.md)) |
| [`deploy/stack/`](deploy/stack/README.md) | Komplett-Installation (Compose): Gateway, MySQL, Telefonbuch, Kommunikation, Sicherung, abgesetzte Sprach-Server | Docker Compose, Caddy | – |
| `.github/` | gemeinsame Workflows und Dependabot | GitHub Actions | – |

**Dokumentation**
- Architektur und Integration: [`phonebook/docs/architecture/ucng-integration.md`](phonebook/docs/architecture/ucng-integration.md)
- Entscheidungen (ADR): [`phonebook/docs/integration-decisions.md`](phonebook/docs/integration-decisions.md)
- Entscheidungen des Release Candidate: [`phonebook/docs/RELEASE-ENTSCHEIDUNGEN.md`](phonebook/docs/RELEASE-ENTSCHEIDUNGEN.md)
- Sicherheit: [`phonebook/docs/security/`](phonebook/docs/security/ph11-review.md)
- Funktionsmatrix: [`phonebook/docs/ucng-feature-parity.csv`](phonebook/docs/ucng-feature-parity.csv)
- UCNG im Modus `phonebook`: [`ucng/docs/phonebook-release-notes.md`](ucng/docs/phonebook-release-notes.md)

## Container-Images

| Image | Quelle | Wofür |
|---|---|---|
| `ghcr.io/<owner>/phonebook` | `phonebook/Dockerfile` | Telefonbuch |
| `ghcr.io/<owner>/ucng` | `ucng/Dockerfile` | Kommunikation |
| `ghcr.io/<owner>/ucng-voice` | `ucng/Dockerfile.voice` | abgesetzte Sprach-Server |
| `ghcr.io/<owner>/ucng-beacon` | `ucng/Dockerfile.beacon` | Beacon (im Modus `phonebook` nicht genutzt) |
| `ghcr.io/<owner>/ucng-bot-music` | `ucng/Dockerfile.music` | Musik-Bot (optional) |

Alle Images gibt es für `linux/amd64` und `linux/arm64`.

### Über GitHub bauen (empfohlen)

1. Repository hochladen, z. B. als `b1tsnetwork/telefonbuch`.
2. *Settings → Actions → General*:
   - Workflows erlauben;
   - für `deps.yml` zusätzlich „Allow GitHub Actions to create and approve pull requests“.
3. Taggen und pushen:
   ```sh
   git tag v1.0.0-rc.1 && git push origin v1.0.0-rc.1
   ```
4. `release.yml` läuft:
   1. Tests beider Teile (`ci.yml`).
   2. Alle fünf Images, Programmdateien, Installationspaket, `SHA256SUMS.txt`.
   3. GitHub Release; ein Tag mit Bindestrich wird als Vorabversion veröffentlicht, ohne `latest`.
5. Bei einem privaten Repository sind die Pakete anfangs privat: unter *Packages → Package settings* die Sichtbarkeit bzw. den Zugriff für die Server freigeben.

Ein Push auf `main` baut die Images mit den Tags `main`, `sha-…` und `latest`.

### Lokal bauen und in eine eigene Registry schieben

```sh
docker login ghcr.io                                    # oder die eigene Registry
./build-images.sh --dry-run                             # zeigt, was gebaut wird
REGISTRY=ghcr.io/b1tsnetwork VERSION=1.0.0-rc.1 ./build-images.sh --push
REGISTRY=registry.firma.local/telefonbuch ./build-images.sh --push --only phonebook,ucng,ucng-voice
```

`--push` baut für `linux/amd64` und `linux/arm64` (Docker Buildx, ggf. `docker buildx create --use`). Ohne `--push` wird nur für die eigene Plattform lokal gebaut.

Die Installation zieht die Images über `TB_IMAGE`/`UCNG_IMAGE` (bzw. `UCNG_VOICE_IMAGE` im Sprach-Server) aus jeder Registry. Alternativ `deploy/stack/compose.build.yml` (baut direkt aus dem Quellcode).

## Workflows (`.github/workflows/`)

| Workflow | Wann | Was |
|---|---|---|
| `ci.yml` | Pull Requests; vor jedem Release | **Telefonbuch:** gofmt, vet, Tests mit MySQL 8.4, govulncheck, JS/PowerShell. **UCNG:** gofmt, vet, Tests (Race), govulncheck, JS, Builds aller Speicher-Varianten mit Rauchtest, Musik-Bot-Image |
| `release.yml` | Push auf `main`, Tags `v*` | CI, dann Images (amd64/arm64, SBOM, Herkunftsnachweis), Programmdateien (Linux/Windows/macOS), Installationspaket, Prüfsummen, GitHub Release |
| `stack.yml` | Änderungen an `deploy/stack`, Dockerfiles, Integrationscode | `docker compose config` aller Profile, `caddy validate`, shellcheck; Start mit echtem MySQL und Caddy (TLS) aus selbst gebauten Images, `check.sh`, Sicherung, abgesetzter Sprach-Server |
| `cla.yml` | Pull Requests | Zustimmung zum CLA von UCNG |
| `deps.yml` | manuell | SQLite-/MySQL-Treiber für UCNG festschreiben (Pull Request) |

## Entwickeln

```sh
# Telefonbuch
cd phonebook && go test -race ./...          # mit MySQL: TB_TEST_DSN='root:…@tcp(127.0.0.1:3306)/telefonbuch'
# Kommunikation
cd ucng && go test -race ./...
```
