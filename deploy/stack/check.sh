#!/bin/sh
# Prüft die laufende Installation von außen durch den Gateway (wie ein Browser):
# getrennte Herkunft, TLS, CSP/Einbettung, Sicherheitsprofil der Kommunikation, kein Gastzugang,
# Live-Verbindungen ohne Proxy-Puffer, WebSocket-Weiterleitung, Betriebssperren.
set -eu
cd "$(dirname "$0")"
# .env legt der Betrieb an (Kopie von .env.example)
# shellcheck source=/dev/null
set -a; . ./.env; set +a
mkdir -p .check
chmod 0750 .check
chgrp 65532 .check 2>/dev/null || true   # ohne root: Hinweis unten bei der Prüfung
ca=""
if [ "${TLS_MODE:-files}" = internal ]; then
	docker compose cp gateway:/data/caddy/pki/authorities/local/root.crt .check/ca.pem >/dev/null
	ca="-ca /check/ca.pem"
elif [ -s config/ca/firma-root-ca.pem ]; then
	cp config/ca/firma-root-ca.pem .check/ca.pem
	ca="-ca /check/ca.pem"
fi
pw=""
if echo "${COMPOSE_PROFILES:-}" | grep -q communication && [ -s secrets/ucng_admin_password ]; then
	install -m 0440 -g 65532 secrets/ucng_admin_password .check/ucng_admin_pw
	pw="-ucng-admin-password-file /check/ucng_admin_pw"
fi
echo "== Dienste"
docker compose ps --format 'table {{.Service}}\t{{.Status}}'
echo "== Prüfung durch den Gateway"
rc=0
# shellcheck disable=SC2086
docker compose run --rm --no-deps -T -v "$PWD/.check:/check:ro" --entrypoint /telefonbuch app \
	commcheck -phonebook "https://$TB_DOMAIN" -communication "https://$COMM_DOMAIN" -connect-to "gateway:443" $ca $pw || rc=$?
rm -f .check/ucng_admin_pw
exit $rc
