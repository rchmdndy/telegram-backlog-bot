# Operations

Copy `deploy/.env.example` to `deploy/.env`, replace the Telegram values, create the backup directory, and run one replica:

```sh
mkdir -p deploy/backups
chmod 700 deploy/backups
sudo chown -R 10001:10001 deploy/backups
cp deploy/.env.example deploy/.env
# set IMAGE_TAG to a full 40-character commit SHA or release digest
chmod 600 deploy/.env
docker compose -f deploy/docker-compose.yml --env-file deploy/.env pull
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up -d
docker compose -f deploy/docker-compose.yml --env-file deploy/.env ps
```

The image uses no published port, a read-only root filesystem, fixed UID/GID 10001, and a persistent `/data` volume. Do not use a network filesystem for SQLite.

## Backup

Use the online backup command; never copy a live SQLite file:

```sh
docker compose -f deploy/docker-compose.yml exec -T bot /backlog-bot backup --output /backups
```

Backups are mode 0600 and contain personal plaintext. Encrypt before off-site storage. Retain seven daily and four weekly copies and periodically test restore.

## Restore

Use this executable procedure. It stops the writer, preserves the current volume as a fallback, validates the selected backup, replaces the database with a mode-0600 copy owned by UID/GID 10001, then starts the pinned image. Keep the fallback until the smoke checks pass.

```sh
set -eu
backup=${1:?usage: $0 /path/to/validated-backup.db}
test -f "$backup"
chmod 600 "$backup"
mkdir -p deploy/backups
# Validate the source before it can replace the live database.
docker run --rm --user 10001:10001 \
  -e DATABASE_PATH=/input/$(basename "$backup") \
  -v "$(dirname "$backup"):/input:ro" \
  ghcr.io/${GITHUB_OWNER:-rchmdndy}/${IMAGE_NAME:-telegram-backlog-bot}:${IMAGE_TAG:?set IMAGE_TAG} \
  /backlog-bot integrity
cp -p "$backup" deploy/backups/restore-source.db

docker compose -f deploy/docker-compose.yml stop bot
docker run --rm --user 10001:10001 -v deploy/backups:/backups -v deploy_backlog-data:/data ghcr.io/${GITHUB_OWNER:-rchmdndy}/${IMAGE_NAME:-telegram-backlog-bot}:${IMAGE_TAG:?set IMAGE_TAG} /backlog-bot backup --output /backups >/dev/null
# Copy only after the stopped-volume fallback backup succeeds.
docker run --rm --user 0:0 -v deploy/backups:/backups -v deploy_backlog-data:/data busybox sh -c 'cp /backups/restore-source.db /data/backlog.db && chown 10001:10001 /data/backlog.db && chmod 600 /data/backlog.db'
docker compose -f deploy/docker-compose.yml up -d bot
docker compose -f deploy/docker-compose.yml ps
```

For an integration smoke test, run the restored image's `healthcheck` after the bot has written its heartbeat, then use the authorized Telegram account to open `/projects` and `/list`. Delete `restore-source.db` and the fallback only after those checks succeed.

The healthcheck requires a fresh atomic heartbeat and a read-only SQLite integrity query. Heartbeat, scheduler, polling, and database-loop failures exit non-zero so Compose can restart the process.
