# Geheimnis des Sprach-Servers

`voice_agent_token` – dieselbe Datei wie `deploy/stack/secrets/voice_agent_token` am Haupt-Server.

```sh
install -o root -g 65532 -m 0440 /pfad/zur/kopie/voice_agent_token secrets/voice_agent_token
```

Wechsel des Schlüssels: am Haupt-Server neu erzeugen (Datei löschen, `sh secrets/create.sh`),
auf alle Sprach-Server verteilen, dann überall `docker compose up -d --force-recreate`.
