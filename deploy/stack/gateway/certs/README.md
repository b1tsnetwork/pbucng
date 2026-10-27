# Zertifikate für den Gateway (TLS_MODE=files)

Hier ablegen (PEM, Zertifikat inklusive Zwischenzertifikaten):

| Datei | Für |
|---|---|
| `tb.crt`, `tb.key` | `TB_DOMAIN` (Telefonbuch) |
| `comm.crt`, `comm.key` | `COMM_DOMAIN` (Kommunikation) |

Die Schlüssel nur für root lesbar machen (`chmod 600 *.key`). Caddy liest sie beim Start.
Mit `TLS_MODE=internal` erzeugt Caddy für Test und Abnahme eine eigene CA; das Stammzertifikat
liegt dann im Volume `caddy-data` unter `caddy/pki/authorities/local/root.crt`.
