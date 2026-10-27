# Zertifikat des Sprach-Servers (TLS_MODE=files)

- `voice.crt` – Zertifikat für VOICE_DOMAIN (mit Zwischenzertifikaten, PEM)
- `voice.key` – privater Schlüssel (PEM, nur für root lesbar: `chmod 0600`)

Die Browser müssen der ausstellenden CA vertrauen (Firmen-CA). Bei `TLS_MODE=internal` erzeugt
Caddy eine eigene CA – nur für Tests, weil die Browser sie sonst nicht kennen.
