# Telegram Backlog Bot

A single-user Telegram backlog bot written in Go with SQLite persistence, project and backlog workflows, Eisenhower recommendations, and container-first VPS deployment.

## Local verification

```sh
go mod tidy
gofmt -w .
go test ./...
go vet ./...
go build ./cmd/backlog-bot
```

Copy `.env.example` to `.env`, supply the bot token and the exact authorized private user/chat IDs, then run the binary. The bot intentionally ignores unauthorized users and all non-private chats.

## VPS deployment

Create a local `deploy/.env` with secrets, create `deploy/backups` owned by UID/GID `10001`, set an immutable `IMAGE_TAG`, and run:

```sh
docker compose -f deploy/docker-compose.yml pull
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml ps
```

The Compose service has no published port, runs non-root with a read-only root filesystem, and persists SQLite in the `backlog-data` volume. Use `docker compose exec -T bot /backlog-bot backup --output /backups` for an application backup; do not copy a live SQLite file directly. Backups contain personal plaintext and should be encrypted before off-site storage.

This v1 keeps a single process/replica by design. Telegram delivery is at-least-once around the unavoidable crash window between a successful Telegram send and the local sent marker.
