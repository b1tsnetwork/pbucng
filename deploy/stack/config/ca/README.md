# Stammzertifikat der Firmen-CA

Für LDAPS zum Active Directory das Stammzertifikat (PEM) hier als `firma-root-ca.pem` ablegen
und in `config/telefonbuch.json` unter `ldap.caFile` eintragen
(`/etc/telefonbuch/ca/firma-root-ca.pem`). Ohne LDAPS (nur lokale Konten) bleibt der Ordner leer.
