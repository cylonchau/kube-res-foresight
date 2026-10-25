# Platform containers

Build targets: `kubeforecast` (the platform; `server` and `job` are sub-commands of it), `operator` in `deploy/docker/Dockerfile`.
Compose: copy `.env.core.example` to `.env`, set database credentials and the JWT
secret, then run `docker compose -f deploy/docker/compose.core.yml up --build -d`.
This uses the host MySQL server; no database service is provisioned automatically.
The migration service is explicit and removes retired-module data during upgrade.
Back up an existing database before deploying the migration.
