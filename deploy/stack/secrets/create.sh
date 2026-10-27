#!/bin/sh
# Legt die Geheimnisse für den Stack an (einmalig, als root im Ordner deploy/stack ausführen).
# Vorhandene Dateien bleiben unverändert. Rechte: Besitzer root, Gruppe 65532 (Telefonbuch-Container;
# die Kommunikation ist per group_add in dieser Gruppe), Modus 0440 – nicht für alle lesbar.
set -eu
cd "$(dirname "$0")"
umask 077
gen() { # Datei Länge
	if [ -s "$1" ]; then echo "  vorhanden: $1"; return; fi
	head -c 96 /dev/urandom | base64 | tr -d '/+=\n' | cut -c1-"$2" > "$1"
	echo "  erzeugt:   $1"
}
ask() { # Datei Frage
	if [ -s "$1" ]; then echo "  vorhanden: $1"; return; fi
	printf '%s: ' "$2"
	stty -echo 2>/dev/null || true
	read -r v
	stty echo 2>/dev/null || true
	echo
	printf '%s' "$v" > "$1"
	echo "  gespeichert: $1"
}
echo "Geheimnisse in $(pwd):"
gen db_password 40
gen db_root_password 40
gen metrics_token 40
gen ucng_integration_secret 64      # gemeinsames Geheimnis Telefonbuch ↔ Kommunikation (≥ 32 Zeichen)
gen ucng_admin_password 32          # Notfallzugang /admin der Kommunikation
gen voice_agent_token 64            # Schlüssel für abgesetzte Sprach-Server (voice-node/)
if [ ! -s ldap_bind_password ]; then
	if [ -t 0 ]; then ask ldap_bind_password "Passwort des LDAP-Dienstkontos (leer = nur lokale Konten)"; else : > ldap_bind_password; echo "  leer:      ldap_bind_password (ohne LDAP)"; fi
fi
chown 0:65532 db_password db_root_password metrics_token ucng_integration_secret ucng_admin_password voice_agent_token ldap_bind_password 2>/dev/null || echo "Hinweis: chown nicht möglich (nicht root?) – Rechte bitte selbst setzen: root:65532, 0440"
chmod 0440 db_password db_root_password metrics_token ucng_integration_secret ucng_admin_password voice_agent_token ldap_bind_password
# Sicherungsordner der Datenbank (Profil backup) gehört dem MySQL-Benutzer im Container (999)
chown 999:999 ../backup/dumps 2>/dev/null && chmod 0750 ../backup/dumps || true
echo "Fertig. Den Schlüssel für Sprach-Server (voice_agent_token) auf jedem voice-node als secrets/voice_agent_token ablegen."
