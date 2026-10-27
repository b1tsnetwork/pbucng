#!/bin/sh
# Tägliche Sicherung der Telefonbuch-Datenbank (Profil „backup“). Läuft im MySQL-Image.
# Dateien: /dumps/telefonbuch-JJJJ-MM-TT_HHMM.sql.gz, ältere als BACKUP_KEEP_DAYS werden gelöscht.
set -eu
KEEP="${BACKUP_KEEP_DAYS:-14}"
AT="${BACKUP_TIME:-02:30}"
dump() {
	f="/dumps/telefonbuch-$(date +%Y-%m-%d_%H%M).sql.gz"
	MYSQL_PWD="$(cat /run/secrets/db_root_password)" mysqldump -h db -u root --single-transaction --routines --triggers --hex-blob telefonbuch | gzip -9 > "$f.tmp"
	mv "$f.tmp" "$f"
	find /dumps -name 'telefonbuch-*.sql.gz' -mtime +"$KEEP" -delete
	echo "$(date -Is) Sicherung: $f ($(du -h "$f" | cut -f1))"
}
[ "${1:-}" = "now" ] && { dump; exit 0; }
echo "Datenbank-Sicherung täglich um $AT, aufbewahrt $KEEP Tage"
while :; do
	now=$(date +%s)
	next=$(date -d "$(date +%F) $AT" +%s 2>/dev/null || echo $((now + 86400)))
	[ "$next" -le "$now" ] && next=$((next + 86400))
	sleep $((next - now))
	dump || echo "$(date -Is) Sicherung FEHLGESCHLAGEN" >&2
done
