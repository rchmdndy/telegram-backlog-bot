# Operations

## Initial VPS setup

The production deployment directory is `/opt/telegram-backlog-bot`. Copy the tracked Compose template there once, then keep runtime secrets only in its root `.env` file:

```sh
cd /opt/telegram-backlog-bot
cp deploy/docker-compose.yml docker-compose.yml
cp deploy/.env.example .env
mkdir -p backups
chmod 700 backups
sudo chown -R 10001:10001 backups
# Set Telegram values and IMAGE_TAG in .env.
chmod 600 .env
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
```

The image uses no published port, a read-only root filesystem, fixed UID/GID 10001, and a persistent `/data` volume. Do not use a network filesystem for SQLite. `TELEGRAM_AUTHORIZED_USER_ID` is mandatory. `TELEGRAM_AUTHORIZED_CHAT_ID` may be omitted for bootstrap: send exactly `/start` from the authorized user's private chat once; the resulting binding is stored in SQLite. Groups, callbacks, and all other updates are ignored until binding. If an explicit chat ID is configured later and differs from the persisted binding, startup fails closed; it never overwrites the database binding.

## Continuous deployment

`Publish GHCR` deploys automatically only after its publish job succeeds for a push to `main`. It uses the published `sha-<40-character-commit-SHA>@sha256:<manifest-digest>` image reference, so production is pinned to the exact manifest built by that workflow rather than `latest`.

Create a GitHub Environment named `production` and add these **Environment secrets**:

```text
VPS_HOST
VPS_PORT
VPS_DEPLOY_USER
VPS_DEPLOY_KEY
VPS_HOST_KEY
```

`VPS_DEPLOY_KEY` is a dedicated deployment private key. `VPS_HOST_KEY` is the complete verified `known_hosts` line for the NAT SSH endpoint, for example:

```text
[108.181.4.225]:21022 ssh-ed25519 AAAA...
```

The value in `VPS_HOST` and `VPS_PORT` must match the host and port fields in `VPS_HOST_KEY` exactly. The workflow pins that key with strict OpenSSH checking, makes an online backup, pulls the published image, recreates the container, waits for Docker health, and rejects startup/fatal error logs. If that sequence fails, it restores the previously configured image reference and reports the deployment failure.

The production Compose file, `.env`, and `backups` directory must remain at `/opt/telegram-backlog-bot`. The deploy user must own the root `.env` file with mode `0600`, because the workflow changes only `IMAGE_TAG` atomically:

```sh
sudo chown botdeploy:botdeploy /opt/telegram-backlog-bot/.env
sudo chmod 600 /opt/telegram-backlog-bot/.env
```

The deployment never runs `docker compose down`, deletes a volume, or copies the live SQLite database.

The GHCR package is currently public, so Docker can pull it without VPS registry credentials. If package visibility changes to private, authenticate the VPS to `ghcr.io` with a read-only package token before enabling deployment.

SQLite migrations are forward-only. Rolling an image back does not reverse a schema migration, so schema-changing releases require a verified compatible backup and restore procedure before deployment.

## Backup

Use the online backup command; never copy a live SQLite file:

```sh
cd /opt/telegram-backlog-bot
docker compose --env-file .env exec -T bot /backlog-bot backup --output /backups
```

Backups are mode 0600 and contain personal plaintext. Encrypt before off-site storage. Retain seven daily and four weekly copies and periodically test restore.

## Restore

Use this executable procedure. It stops the writer, preserves the current volume as a fallback, validates the selected backup, replaces the database with a mode-0600 copy owned by UID/GID 10001, then starts the pinned image. Keep the fallback until the smoke checks pass.

```sh
set -eu
cd /opt/telegram-backlog-bot
backup=${1:?usage: $0 /path/to/validated-backup.db}
test -f "$backup"
chmod 600 "$backup"
# Validate the source before it can replace the live database.
docker run --rm --user 10001:10001 \
  -e DATABASE_PATH=/input/$(basename "$backup") \
  -v "$(dirname "$backup"):/input:ro" \
  "$(docker compose --env-file .env config --images | head -1)" \
  /backlog-bot integrity
cp -p "$backup" backups/restore-source.db

docker compose --env-file .env stop bot
docker run --rm --user 10001:10001 -v "$PWD/backups:/backups" -v telegram-backlog-bot_backlog-data:/data "$(docker compose --env-file .env config --images | head -1)" /backlog-bot backup --output /backups >/dev/null
# Copy only after the stopped-volume fallback backup succeeds.
docker run --rm --user 0:0 -v "$PWD/backups:/backups" -v telegram-backlog-bot_backlog-data:/data busybox sh -c 'cp /backups/restore-source.db /data/backlog.db && chown 10001:10001 /data/backlog.db && chmod 600 /data/backlog.db'
docker compose --env-file .env up -d bot
docker compose --env-file .env ps
```

For an integration smoke test, run the restored image's `healthcheck` after the bot has written its heartbeat, then use the authorized Telegram account to open `/projects` and `/list`. Delete `restore-source.db` and the fallback only after those checks succeed.

The healthcheck requires a fresh atomic heartbeat and a read-only SQLite integrity query. Heartbeat, scheduler, polling, and database-loop failures exit non-zero so Compose can restart the process.
