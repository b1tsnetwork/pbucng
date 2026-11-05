# Grunddaten der E-Mail-Signatur

Hier liegen die eigenen Grunddaten der Signatur. Sie gehören nicht ins Projekt und werden nicht eingecheckt (`.gitignore`).

1. Vorlage kopieren: `phonebook/docs/signature.example.json` → `config/signatur/signatur.json`
2. Ausfüllen: Organisation, Vorwahl, Postanschrift, Links, Dienstgebäude. Optional `unitSites` mit den Kürzeln der Einheiten.
3. Logo als PNG daneben legen und eintragen: `"logoFile": "logo.png"`. Doppelte Anzeigegröße, z. B. 310 × 90 für 155 × 45, höchstens 40 KB.
4. In `.env`: `TB_SIGNATURE_FILE=/etc/telefonbuch/signatur/signatur.json`, dann `docker compose up -d app`.

Die Datei wird beim Start geprüft. Fehler (Tippfehler in Feldnamen, fehlendes Logo, ungültige Werte) verhindern den Start, die Meldung steht in `docker compose logs app`.

Was in der Verwaltung des Telefonbuchs gespeichert wird („✉ Signatur verwalten“), gilt vor dieser Datei. „Auf Datei zurücksetzen“ verwirft es wieder.
