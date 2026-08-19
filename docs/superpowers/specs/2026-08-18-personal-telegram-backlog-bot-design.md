# Personal Telegram Backlog Bot — Design Blueprint

**Status:** Approved design  
**Date:** 2026-08-18  
**Target:** Personal, single-user Telegram bot deployed as one container on a VPS

## 1. Product Goal

Build a personal Telegram bot for managing multiple projects and their backlog items. The bot provides complete project and backlog CRUD, captures priorities using the Eisenhower Matrix, and sends a concise daily recommendation at 06:00 Asia/Jakarta (UTC+7).

The interaction should feel native to Telegram:

- Use inline keyboards whenever the answer comes from a finite, standardized set.
- Request free text only for values such as a name, title, notes, or date.
- Show a confirmation preview before mutations that create, update, archive, reopen, complete, or permanently delete data.
- Keep messages easy to scan using consistent icons, headings, compact metadata, and pagination.

## 2. Scope

### 2.1 Version 1 includes

- A single authorized Telegram user.
- Project lifecycle management: create, read, update, archive, and restore. Permanent project deletion is intentionally unavailable.
- CRUD for backlog items, including explicit permanent deletion.
- Project archive and restore.
- Backlog completion, reopening, and explicit permanent deletion.
- Eisenhower quadrant selection for every backlog item.
- A mandatory deadline for every backlog item.
- A daily recommendation at 06:00 Asia/Jakarta with at most 10 active items.
- Manual access to the same “focus today” recommendation.
- Persistent conversational wizard state across application restarts.
- Containerized VPS deployment using an image pulled from GitHub Container Registry (GHCR).
- Automated testing, multi-architecture image build, security scanning, and GHCR publishing through GitHub Actions.
- SQLite backup and restore procedures.

### 2.2 Explicitly out of scope for version 1

- Multiple users, teams, roles, or shared workspaces.
- Web dashboard or public HTTP API.
- Attachments, labels, subtasks, recurring backlog items, estimates, or time tracking.
- Natural-language or artificial-intelligence prioritization.
- Redis, message brokers, microservices, PostgreSQL, or horizontal scaling.
- Telegram webhook mode.
- Automatic VPS deployment over SSH. The pipeline publishes the image; the VPS operator pulls and restarts it.

## 3. Confirmed Product Decisions

- **Runtime:** Go.
- **Storage:** SQLite.
- **Telegram transport:** long polling.
- **Architecture:** modular monolith in one application process.
- **Access:** one Telegram user ID and one private Telegram chat ID, both explicitly configured through the VPS environment outside the repository.
- **Telegram parse mode:** HTML with strict escaping of all user-generated content.
- **SQLite driver:** `modernc.org/sqlite` (pure Go) to keep reproducible CGO-free `linux/amd64` and `linux/arm64` builds.
- **License:** MIT.
- **Default branch:** `main`.
- **Timezone:** `Asia/Jakarta`.
- **Notification time:** 06:00 local time.
- **Daily limit:** 10 items globally, grouped by project after selection.
- **Deadline:** mandatory.
- **Completed backlog:** retained with status `done`.
- **Deleted project:** represented by archive status; related backlog history remains intact.
- **Container registry:** GHCR.
- **Source repository:** public GitHub repository.
- **Image publishing:** GitHub Actions using `GITHUB_TOKEN` and `packages: write`.

## 4. System Architecture

The system runs as a modular monolith: one Go binary contains the Telegram adapter, conversation state machine, application services, persistence adapters, priority engine, and scheduler. SQLite runs in-process through a Go driver and stores its database file in a mounted Docker volume.

```text
Telegram Bot API
      ⇅ long polling
┌──────────────────────────────────────────────┐
│ One Go application container                │
│                                              │
│ Telegram update router                       │
│ Conversation state machine                   │
│ Project application service                  │
│ Backlog application service                  │
│ Daily recommendation engine                  │
│ Scheduler with an injectable clock           │
│ SQLite repositories and migrations           │
│                                              │
│ /data/backlog.db ───── Docker named volume   │
└──────────────────────────────────────────────┘
```

### 4.1 Module boundaries

1. **Configuration**
   - Loads and validates environment variables.
   - Fails fast on missing token, invalid authorized user ID, timezone, database path, or notification settings.

2. **Telegram adapter**
   - Receives updates using long polling.
   - Accepts only `chat.type = private`, `from.id = TELEGRAM_AUTHORIZED_USER_ID`, and `chat.id = TELEGRAM_AUTHORIZED_CHAT_ID`; both IDs are mandatory signed 64-bit environment values and startup fails when either is missing or invalid.
   - Silently ignores unauthorized users and group/channel contexts without revealing whether private data exists.
   - Processes updates serially for the one authorized user.
   - Maps commands, messages, and callback queries into application actions.
   - Uses Telegram HTML parse mode and escapes all user-generated content through one renderer helper.

3. **Conversation state machine**
   - Controls multi-step create and edit wizards.
   - Stores the current state and draft payload in SQLite.
   - Supports `/cancel` and explicit Back/Cancel buttons.
   - Rejects callbacks that do not match the active state or expected entity.

4. **Project service**
   - Validates project names.
   - Creates, lists, updates, archives, and restores projects.
   - Excludes archived projects from normal selection and recommendations.

5. **Backlog service**
   - Creates, lists, updates, completes, reopens, and permanently deletes backlog items.
   - Enforces project activity, mandatory deadlines, valid status transitions, and valid quadrants.

6. **Recommendation engine**
   - Produces a deterministic global ordering.
   - Selects no more than the configured daily limit.
   - Groups selected results by project without changing their global rank.

7. **Scheduler**
   - Uses `Asia/Jakarta`, independent of the host timezone.
   - Triggers the daily recommendation at 06:00.
   - Prevents duplicate sends through a persisted notification record.
   - Supports catch-up after downtime with a defined cutoff.

8. **SQLite persistence**
   - Owns migrations, transactions, and repository implementations.
   - Uses foreign keys, WAL journaling, a busy timeout, and bounded connections.

Dependencies point inward: Telegram and SQLite are adapters around domain and application services. Services depend on small repository, clock, and notifier interfaces so business behavior can be tested without Telegram or a real clock.

## 5. Data Model

All technical timestamps are stored as signed 64-bit Unix microseconds in UTC, avoiding mixed text precision and lexical-order ambiguity. Business dates such as a deadline are stored as an ISO local date (`YYYY-MM-DD`) because the product treats a deadline as a calendar day in `Asia/Jakarta`, not an instant.

Use SQLite migrations with monotonically increasing versions. Foreign keys are enabled for every connection.

### 5.1 `projects`

| Column | Type | Rule |
|---|---|---|
| `id` | TEXT | UUID primary key |
| `name` | TEXT | Required, trimmed |
| `description` | TEXT | Optional |
| `status` | TEXT | `active` or `archived` |
| `created_at` | INTEGER | UTC Unix microseconds |
| `updated_at` | INTEGER | UTC Unix microseconds |
| `archived_at` | INTEGER | Nullable UTC Unix microseconds |

Project names are trimmed and normalized with Unicode case folding in the application; a `normalized_name` column and partial unique index enforce uniqueness among active projects. An archived project may retain the same display name as a later active project. Restore is blocked when its normalized name conflicts with an active project, and the bot offers Rename or Cancel.

Domain and database invariants require `status = archived` exactly when `archived_at` is non-null. Restoring clears `archived_at`. Every mutation refreshes `updated_at`.

### 5.2 `backlog_items`

| Column | Type | Rule |
|---|---|---|
| `id` | TEXT | UUID primary key |
| `project_id` | TEXT | Required foreign key to `projects` |
| `title` | TEXT | Required, trimmed |
| `notes` | TEXT | Optional |
| `quadrant` | TEXT | `q1`, `q2`, `q3`, or `q4` |
| `deadline_date` | TEXT | Required ISO local date |
| `status` | TEXT | `active` or `done` |
| `created_at` | INTEGER | UTC Unix microseconds |
| `updated_at` | INTEGER | UTC Unix microseconds |
| `completed_at` | INTEGER | Nullable UTC Unix microseconds |

A backlog item remains attached to its project until explicitly moved. `status = done` requires a non-null `completed_at`; `status = active` requires `completed_at = NULL`. Every mutation refreshes `updated_at`.

Archiving a project hides all of its backlog from normal lists and recommendations but preserves history in the archived-project detail. Items under an archived project remain readable and can be completed; other edits, reopening, or moving them require restoring the project first. This keeps history accessible without allowing archived work to re-enter active workflows accidentally.

### 5.3 `conversation_states`

| Column | Type | Rule |
|---|---|---|
| `telegram_user_id` | INTEGER | Primary key; authorized user only |
| `flow` | TEXT | Current wizard, such as `create_backlog` |
| `step` | TEXT | Current expected input |
| `draft_json` | TEXT | Validated JSON draft |
| `draft_id` | TEXT | UUID used as an idempotency key |
| `draft_version` | INTEGER | Optimistic-lock version |
| `schema_version` | INTEGER | Draft JSON schema version |
| `updated_at` | INTEGER | UTC Unix microseconds |
| `expires_at` | INTEGER | UTC Unix microseconds |

Only one wizard can be active for the user. Updates are serialized for the authorized user and draft writes additionally use optimistic version checks. Starting another wizard asks whether to discard the current draft. Drafts expire after 24 hours; an expired or incompatible draft is removed and the user receives a friendly restart action.

### 5.4 `notification_runs`

| Column | Type | Rule |
|---|---|---|
| `local_date` | TEXT | Primary key, ISO date in Asia/Jakarta |
| `scheduled_for` | INTEGER | UTC Unix microseconds corresponding to 06:00 local |
| `status` | TEXT | `pending`, `sending`, `sent`, or `failed` |
| `attempt_count` | INTEGER | Non-negative |
| `last_error` | TEXT | Nullable, sanitized |
| `sent_at` | INTEGER | Nullable UTC Unix microseconds |
| `created_at` | INTEGER | UTC Unix microseconds |
| `updated_at` | INTEGER | UTC Unix microseconds |

The local date is the run idempotency key. The selected recommendation is snapshotted once so retries cannot change its content.

### 5.5 `notification_run_items`

| Column | Type | Rule |
|---|---|---|
| `local_date` | TEXT | Foreign key to `notification_runs` |
| `ordinal` | INTEGER | Global rank, unique within the run |
| `backlog_item_id` | TEXT | Nullable reference retained for actions |
| `project_name` | TEXT | Escaped at render time; snapshot value |
| `title` | TEXT | Snapshot value |
| `quadrant` | TEXT | Snapshot value |
| `deadline_date` | TEXT | Snapshot value |

The snapshot drives retries and the “Tandai Selesai” selector. If an item has since been completed, deleted, moved, or archived, the action shows its current state and skips invalid mutations instead of changing another item.

### 5.6 `notification_parts`

| Column | Type | Rule |
|---|---|---|
| `local_date` | TEXT | Foreign key to `notification_runs` |
| `part_index` | INTEGER | Zero-based, unique within run |
| `payload_json` | TEXT | Immutable rendered text and keyboard payload |
| `status` | TEXT | `pending` or `sent` |
| `telegram_message_id` | INTEGER | Nullable |
| `sent_at` | INTEGER | Nullable UTC Unix microseconds |

Parts are created in the same transaction as the run and item snapshot. The unique key is `(local_date, part_index)`. Payloads are immutable once sending begins; recovery resumes at the first locally pending part.

### 5.7 `processed_updates` and `mutation_receipts`

`processed_updates` stores Telegram `update_id` as its primary key with `processed_at` in UTC Unix microseconds. `mutation_receipts` stores a unique mutation nonce, action, entity ID/result JSON, and processed time.

Update handling for the single user is serialized. A mutating callback carries a nonce tied to the current draft or confirmation screen. In one transaction, the service validates the entity version/status, performs the mutation, inserts the receipt, and marks the Telegram update processed. Replays with the same update ID or mutation nonce return the stored result without mutating again. Replays with a different nonce are safe through domain preconditions: complete/archive/delete are idempotent for the target state, while conflicting edit/reopen/restore requests fail with a fresh detail view. Create confirmations consume the unique `draft_id`, so repeated callbacks return the already-created entity. Receipts are retained for 90 days; processed update rows older than 30 days may be pruned because Telegram update IDs increase monotonically.

### 5.8 Configuration rather than mutable settings

For version 1, access identity, timezone, notification time, and recommendation limit are environment-driven, not user-editable database settings. `RECOMMENDATION_LIMIT` defaults to `10` and must be an integer from `1` through `10`. This prevents a second source of truth. A future settings wizard may move these values into a settings table.

## 6. Eisenhower Priority and Recommendation Rules

### 6.1 Quadrants

| Rank | Quadrant | Meaning | Display |
|---|---|---|---|
| 1 | `q1` | Urgent and important | 🔴 Do now |
| 2 | `q2` | Important, not urgent | 🔵 Schedule |
| 3 | `q3` | Urgent, not important | 🟠 Delegate/minimize |
| 4 | `q4` | Neither urgent nor important | ⚪ Reconsider |

The user explicitly chooses a quadrant. The bot does not silently infer or mutate it from the deadline.

### 6.2 Eligibility

An item is eligible only when:

- its status is `active`;
- its project status is `active`; and
- it has a valid mandatory deadline.

### 6.3 Deterministic global ordering

At recommendation time, eligible items are sorted by this tuple:

1. **Deadline bucket:** overdue first; due today second; future third.
2. **Quadrant rank:** Q1, Q2, Q3, Q4.
3. **Deadline date:** earliest first.
4. **Created timestamp:** oldest first.
5. **Item ID:** lexical ascending final tie-breaker.

This gives overdue work priority while preserving the Eisenhower ordering inside each deadline bucket. It avoids an ambiguous numerical score and produces stable testable output.

The engine selects the first 10 items globally. The message then groups the selected items by project. Projects are ordered by the best-ranked selected item they contain, and each item retains its global ordinal number.

### 6.4 Empty result

If no active item is eligible, the bot still sends one compact message at 06:00 stating that there is no planned backlog and offering buttons to add a backlog item or open projects. This confirms that the scheduler is operating.

## 7. Telegram Interaction Design

### 7.1 Main menu

`/start` and `/menu` display:

- `➕ Tambah Backlog`
- `📋 Daftar Backlog`
- `📁 Projects`
- `☀️ Fokus Hari Ini`
- `❓ Bantuan`

Registered commands also include `/cancel` and `/help`. Buttons are preferred for navigation, but commands provide recovery if an old message is no longer usable.

### 7.2 Create project wizard

1. Ask for project name as free text.
2. Ask for optional description as free text with `Lewati` and `Batal` buttons.
3. Show a preview.
4. Confirm with `Simpan`, `Ubah Nama`, `Ubah Deskripsi`, or `Batal`.

### 7.3 Project list and detail

- List active projects with inline pagination.
- Provide a separate archived-project view.
- Project detail shows active/done counts and actions:
  - `Lihat Backlog`
  - `Ubah Nama`
  - `Ubah Deskripsi`
  - `Arsipkan` or `Pulihkan`
  - `Kembali`
- Archiving requires confirmation and clearly states that active backlog under the project disappears from daily recommendations.

### 7.4 Create backlog wizard

1. Ask for title as free text.
2. Choose an active project using buttons and pagination; include `Buat Project Baru` and return to the draft afterward.
3. Choose one of four Eisenhower buttons.
4. Choose a deadline:
   - `Hari ini`
   - `Besok`
   - `7 hari lagi`
   - `Masukkan tanggal`
   - `Batal`
5. When entering a date, accept strict, documented forms: `YYYY-MM-DD` or `DD-MM-YYYY`. Echo the normalized Indonesian date and ask for correction on invalid or past dates. A past date is rejected when creating a new item; editing an existing overdue item may retain or move its deadline.
6. Ask for optional notes with `Lewati` and `Batal`.
7. Show a complete preview.
8. Confirm with `Simpan`, field-specific edit buttons, or `Batal`.

### 7.5 Backlog list and detail

- Default list shows active items ordered by the recommendation comparator.
- Filters use buttons: project, quadrant, status, and deadline bucket.
- Use stable offset pagination with 8 entities per page. Callback payloads use a compact versioned action code, short entity reference, page number, and draft nonce; they must remain at or below Telegram's 64-byte limit. Pages are always re-queried, so deleted or archived entities yield a refreshed page rather than trusting stale content.
- Detail actions:
  - `✅ Tandai Selesai`
  - `✏️ Ubah Judul`
  - `📁 Pindah Project`
  - `🎯 Ubah Prioritas`
  - `📅 Ubah Deadline`
  - `📝 Ubah Catatan`
  - `🗑️ Hapus Permanen`
  - `Kembali`
- Each edit action opens a one-field wizard, validates the new value with the same create rules, presents old versus new values, and requires `Simpan Perubahan` or `Batal`. The entity and its `updated_at` version are reloaded at confirmation; conflicting changes abort with a fresh detail view.
- Done items are readable and provide `Buka Kembali`; other fields are not editable until reopened.
- The operation matrix is explicit: active item + active project supports read/edit/complete/move/delete; done item + active project supports read/reopen/delete; active item + archived project supports read/complete only; done item + archived project is read-only. Restore the project before reopening, editing, moving, or deleting an archived-project item.
- Permanent deletion requires two explicit actions: open delete confirmation, then press `Ya, hapus permanen`. It is never implied by project archival.

### 7.6 Input limits

Inputs are trimmed with Unicode whitespace, normalized to NFC, and then measured in Unicode code points before HTML escaping. Display values preserve case; only the separate project uniqueness key uses Unicode case folding. Limits are:

- project name: 1–80;
- project description: 0–500;
- backlog title: 1–160;
- backlog notes: 0–2,000.

The bot reports the relevant limit without discarding the draft. The renderer tests special HTML characters (`<`, `>`, `&`, quotes), Markdown-like characters, emoji, combining characters, and right-to-left text.

### 7.7 Callback safety

Telegram callback payloads are compact action and entity references; they never contain trusted mutable data. On every callback the application:

- verifies the authorized user;
- reloads the entity;
- verifies expected status and wizard state;
- rejects stale callbacks with a friendly message and a fresh navigation button;
- answers the callback query promptly to stop Telegram’s loading indicator.

## 8. Daily Notification

At 06:00 `Asia/Jakarta`, the scheduler renders a message like:

```text
☀️ Selamat pagi! Fokus hari ini
Selasa, 18 Agustus 2026 · 3 backlog dipilih

🤖 Backlog Bot
1. 🔴 Perbaiki scheduler
   ⚠️ Terlambat 1 hari
2. 🔵 Tambah backup SQLite
   📅 20 Agu

🌐 Website
3. 🔴 Perbaiki autentikasi
   📅 Hari ini

Ringkasan: 1 overdue · 2 Q1 · 2 project
```

Buttons:

- `✅ Tandai Selesai` opens a paginated selection of the items in this recommendation.
- `📋 Buka Backlog` opens the active backlog list.
- `➕ Tambah Backlog` is shown when the recommendation is empty.

Telegram message length is checked before sending. If project names, titles, and notes would exceed the platform limit, the renderer splits at project boundaries and labels each part; buttons are placed on the final part. Notes are omitted from daily summaries.

## 9. Scheduler Semantics and Failure Handling

- The application loads the IANA timezone `Asia/Jakarta` and calculates the next local 06:00 occurrence.
- The clock is injectable for deterministic tests.
- On startup:
  - before 06:00, schedule normally;
  - from 06:00 through 12:00, send a catch-up notification if today has no `sent` notification run;
  - after 12:00, do not send a stale morning message; schedule the next day.
- Only one process/replica is supported. SQLite and the single-replica deployment are explicit constraints.
- The delivery contract is **at least once**, not exactly once. Telegram provides no idempotency key that can atomically coordinate with SQLite; a crash after Telegram accepts a message but before the local commit can produce a duplicate. The message includes the local date so a rare duplicate is recognizable.
- The recommendation snapshot is created once and split into ordered message parts. Each part records `pending` or `sent` progress. Retries never recalculate ranking and resume from the first locally unsent part, while acknowledging that the crash window can still duplicate that part.
- For transient network and Telegram 5xx errors, attempt immediately and retry up to 4 times with base delays of 1, 2, 4, and 8 seconds plus up to 25% jitter. Telegram 429 honors `Retry-After` when it remains inside the 12:00 cutoff. Telegram 4xx errors other than 429 are permanent.
- After immediate retry exhaustion, the run remains `failed`; a maintenance tick every 15 minutes retries it until 12:00 local. `sending` rows older than 10 minutes are recovered as failed on startup/tick.
- The scheduler marks a run `sent` only after every message part is locally recorded as sent. At 12:00 exactly, no new attempt starts; an attempt already in progress may finish.
- Graceful shutdown stops polling, prevents new work, waits for current handlers within a timeout, closes the database, and exits.

## 10. SQLite Operation

Database path: `/data/backlog.db` in production. The implementation uses the pure-Go `modernc.org/sqlite` driver, and the same driver is exercised by local, CI, container, amd64, and arm64 tests.

Connection initialization applies:

- `PRAGMA foreign_keys = ON` on every connection;
- WAL journal mode;
- a non-zero busy timeout;
- synchronous mode suitable for durability (`NORMAL` with WAL for this workload);
- bounded connection pool, with one open connection as the safe version 1 default.

All multi-row mutations and notification state transitions use transactions. Schema changes happen only through embedded, versioned migrations executed before polling starts. The application refuses to start if migration fails.

SQLite must live on a local Docker volume or local host bind mount with correct filesystem locking. Network filesystems are unsupported.

## 11. Security and Privacy

- The bot processes only private-chat updates whose sender matches `TELEGRAM_AUTHORIZED_USER_ID` and whose chat matches the bound private chat ID. The authorized user cannot expose data by invoking the bot in a group.
- `TELEGRAM_BOT_TOKEN` and authorized user ID are runtime secrets and are never committed, embedded in image layers, printed, or included in telemetry.
- `.env.example` contains placeholders only; `.env` is ignored by Git.
- Logs are structured and omit message bodies, callback payload drafts, token values, and backlog notes by default.
- User-generated text is escaped before Telegram rendering.
- Input length limits are enforced for names, titles, descriptions, and notes.
- The container runs as a non-root user, with a read-only root filesystem and only `/data` writable.
- The public repository uses dependency scanning, static analysis, image vulnerability scanning, and secret scanning available through GitHub.
- Dependencies are pinned through `go.mod`/`go.sum`, and GitHub Actions are pinned to immutable major versions at minimum; commit-SHA pinning is preferred during implementation review.

## 12. Observability and Health

The service writes structured logs to standard output with level, event name, and safe identifiers. Docker handles bounded log rotation.

Because long polling does not require an exposed public HTTP endpoint, version 1 uses a container healthcheck implemented as a binary subcommand such as `backlog-bot healthcheck`. It verifies:

- the main process heartbeat is recent; and
- SQLite can execute a lightweight query.

Immediately after migrations and before long polling, the main loop writes JSON containing process start time and last-success timestamp to `/data/.heartbeat.tmp`, fsyncs/closes it, and atomically renames it to `/data/.heartbeat`; it repeats every 15 seconds. A permission or write failure is fatal and exits non-zero. Docker runs the healthcheck every 30 seconds with a 5-second timeout, 3 retries, and a 20-second start period; missing/malformed heartbeat, a timestamp older than 90 seconds, or a failed read-only SQLite query is unhealthy. Health status is observability only because Compose does not restart an unhealthy process. Fatal polling, scheduler, heartbeat, or database-loop failures cause the main process to exit non-zero, allowing `restart: unless-stopped` to restart it. No port is published by the production Compose file.

Important log events include startup, migration version, polling recovery, wizard errors, notification run lifecycle, backup lifecycle, and graceful shutdown. Raw secrets and personal backlog content are excluded.

## 13. Container and VPS Deployment

### 13.1 Image

A multi-stage Dockerfile:

1. Builds a static or minimally linked Go binary in a pinned Go builder image.
2. Embeds IANA timezone data through Go's `time/tzdata` package and copies required CA certificates into a pinned minimal runtime image, so `Asia/Jakarta` never depends on host tzdata.
3. Runs as a fixed non-root UID/GID.
4. Declares `/data` as the writable data path.
5. Includes a container healthcheck.
6. Supports `linux/amd64` and `linux/arm64`.

### 13.2 VPS Compose

The repository includes a production Compose example that:

- references `ghcr.io/${GITHUB_OWNER}/${IMAGE_NAME}:${IMAGE_TAG}` rather than building locally, with `IMAGE_NAME=backlog-bot` and a release tag/digest supplied in the VPS-local environment;
- uses `restart: unless-stopped`;
- runs with fixed UID/GID `10001:10001`, mounts a named volume at `/data`, and mounts a host directory such as `./backups:/backups`; setup documentation creates both writable paths with owner `10001:10001`, backup directory mode `0700`, and backup files mode `0600`;
- sets `BACKUP_DIR=/backups` and passes secrets through a VPS-local `.env`;
- sets the root filesystem read-only;
- mounts a size-limited `tmpfs` at `/tmp`;
- drops unnecessary Linux capabilities;
- configures bounded JSON log rotation;
- does not expose any network port;
- documents a one-replica-only rule.

Normal deployment:

```bash
docker compose pull
docker compose up -d
docker compose ps
```

Rollback pins the previous semantic image tag or digest and reruns the same commands. Production documentation recommends pinning a release tag or digest instead of relying on `latest`.

## 14. Backup and Restore

A raw file copy of an active SQLite database is not the supported backup method.

The application provides `backlog-bot backup --output /backups` using SQLite's online backup mechanism. It writes a timestamped file with mode `0600`, runs `PRAGMA integrity_check` against the copy, emits a safe summary, and exits non-zero on any failure. A host cron invokes `docker compose exec -T bot backlog-bot backup --output /backups`; retention is handled by a documented host script. Off-site copies must be encrypted by the operator because backups contain personal plaintext data.

Recommended VPS schedule:

- run a backup daily after the morning notification window;
- retain 7 daily and 4 weekly copies;
- copy backups off the VPS using an operator-managed mechanism;
- periodically test restore into a temporary volume.

Restore procedure:

1. Stop the bot container.
2. Preserve the current database volume as a fallback.
3. Restore the selected validated backup to `/data/backlog.db` with correct ownership.
4. Start the pinned application image.
5. Confirm migration status, health, project list, and backlog list.

## 15. GitHub Repository and CI/CD

Creating the public repository and publishing packages are outward-facing actions. They occur during implementation after the written specification is reviewed. The planned repository contains source, tests, migrations, Docker assets, workflow files, operator documentation, an MIT license, and no secrets.

### 15.1 Pull-request workflow

On pull requests and pushes to the default branch:

- set up the pinned Go toolchain;
- verify formatting;
- run `go vet` and a pinned `golangci-lint` release configured in the repository;
- run unit and integration tests, including the race detector where compatible;
- build the binary;
- build the container without publishing on pull requests;
- run `govulncheck` against Go source/dependencies and fail on reachable vulnerabilities;
- run pinned Gitleaks against repository history and fail on verified secret findings;
- scan the built image with pinned Trivy;
- upload useful test/scan summaries without exposing data.

A failed test, lint, `govulncheck`, Gitleaks, or build blocks publishing. Trivy blocks publication on fixable `CRITICAL` vulnerabilities; `HIGH` findings are reported in the workflow summary for review. This initial image policy can be tightened after the first dependency baseline is known. GitHub Dependabot is enabled for Go modules, Docker base images, and GitHub Actions.

### 15.2 GHCR publish workflow

Publishing uses a workflow concurrency group keyed by the ref, with cancellation disabled once publication starts. It runs on:

- successful pushes to `main` for a full 40-character commit SHA tag plus `latest`; and
- semantic tags such as `v1.2.3` for the exact `v1.2.3` and `1.2.3` aliases only.

Major/minor floating aliases (`1` and `1.2`) are intentionally omitted in version 1. `latest` may move only from a successful `main` build; release aliases may be created only once, and the workflow fails if the remote alias already resolves to another digest. The summary lists every alias and the single manifest digest they resolve to.

The workflow:

1. Uses job-scoped least privilege: build/publish receives `contents: read` and `packages: write`; provenance receives `id-token: write` and `attestations: write`; SARIF upload, if enabled for a public repository, receives `security-events: write`. Pull-request jobs from forks receive read-only permissions and never publish.
2. Logs in to `ghcr.io` using the repository `GITHUB_TOKEN`. All third-party actions are pinned to reviewed full commit SHAs.
3. Builds for `linux/amd64` and `linux/arm64` with Buildx.
4. Publishes one multi-platform manifest.
5. Adds Open Container Initiative labels linking the image to the public repository and commit.
6. Generates an SBOM and provenance attestation.
7. Scans the final image with pinned Trivy, failing on fixable `CRITICAL` vulnerabilities and reporting `HIGH` findings.
8. Exposes the resulting digest in the workflow summary.

No personal access token is committed or required for normal publication. GHCR package visibility is set to public and linked to the repository. If GitHub does not inherit visibility automatically, this is performed explicitly using authenticated `gh`/GitHub API after package creation.

### 15.3 Release discipline

- `latest` is convenient for development but is not the recommended production pin.
- A Git tag and an image tag are mutable references even when policy says not to move them; the only immutable reference is the published digest.
- VPS deployments should use a semantic tag with a recorded digest, or pin the digest directly.
- Database migrations are forward-only. A release that changes schema must document rollback limitations and require a verified backup before upgrade.

## 16. Test Strategy

### 16.1 Unit tests

- Eisenhower comparator and stable tie-breaking.
- Overdue/today/future bucketing around midnight in Asia/Jakarta.
- Date parsing and validation.
- Project and backlog state transitions.
- Message rendering, escaping, grouping, and splitting.
- Scheduler next-run and catch-up rules using a fake clock.

### 16.2 Repository and migration tests

- Run against a temporary SQLite database.
- Apply migrations from empty state.
- Verify constraints, transactions, archive behavior, completion, reopen, delete, and notification idempotency.
- Verify upgrading from every supported schema fixture to the current schema.

### 16.3 Handler and conversation tests

- Use a fake Telegram client.
- Cover happy paths, every edit wizard, invalid text, stale callbacks, back/cancel, 24-hour expiration, restart/resume, private-chat enforcement, unauthorized users, group messages from the authorized user, duplicate update IDs, double-click confirmation, concurrent input serialization, and failure responses.
- Assert standardized choices use buttons rather than requiring arbitrary text.

### 16.4 Integration and container tests

- Run the main CRUD journeys through the update handler with real SQLite and fake Telegram transport.
- Start the built image with a temporary volume, run healthcheck, and verify graceful shutdown.
- Validate that data survives container replacement.
- Validate backup and restore.
- Build and smoke-test the same production driver/image for `linux/amd64` and `linux/arm64`, including embedded `Asia/Jakarta` timezone data.
- Verify notification snapshot recovery, partial-send retry behavior, and the documented rare duplicate window after a simulated crash.

## 17. Error-Handling Experience

User-facing errors are concise, actionable, and do not expose internals. Examples:

- Invalid date: show accepted formats and retain the wizard draft.
- Stale button: explain that the menu is outdated and provide `Buka Menu Terbaru`.
- Archived project: explain that it cannot receive a new backlog until restored.
- Database busy beyond timeout: apologize, preserve state, and offer `Coba Lagi`.
- Telegram transport error: retry in the background and avoid duplicate mutations.

Unexpected errors receive a correlation ID in logs and a generic Telegram response. Mutations are transactionally committed before success is displayed.

## 18. Delivery Strategy

Implementation is divided into reviewable milestones:

1. **Repository foundation:** Git initialization, Go module, configuration, logging, migrations, SQLite connection, domain types, test harness, and documentation skeleton.
2. **Project management:** project service, repositories, wizard handlers, archive/restore, and tests.
3. **Backlog management:** backlog service, full wizard, list/detail/filter/pagination, completion/reopen/delete, and tests.
4. **Recommendation and scheduling:** deterministic engine, renderer, scheduler, idempotency, catch-up, and tests.
5. **Operational hardening:** backup/restore commands, healthcheck, graceful shutdown, security controls, and container persistence tests.
6. **Container and automation:** Dockerfile, Compose examples, GitHub Actions test/build/scan/publish workflows, SBOM, provenance, and GHCR documentation.
7. **Public delivery:** create the approved public GitHub repository with `gh`, push the reviewed code, verify Actions, make GHCR public, and verify a clean VPS-style pull/run using a non-secret test configuration.

Each milestone must pass tests and a separate review before the next outward-facing publication step.

## 19. Acceptance Criteria

The version 1 design is complete when:

- An unauthorized Telegram account cannot read or mutate data, and even the authorized user cannot expose data from a group/channel context.
- Duplicate update IDs and repeated confirmation callbacks do not duplicate project/backlog mutations.
- The authorized user can create, list, edit, archive/restore projects using interactive menus; restore conflicts require rename or cancel, and project permanent deletion is unavailable.
- The authorized user can create, list, filter, edit, complete, reopen, and explicitly delete backlog items.
- Every created backlog item has a project, quadrant, and valid deadline.
- Wizard state survives a normal container restart.
- At 06:00 Asia/Jakarta the bot creates only one recommendation snapshot containing at most 10 globally ranked items, grouped by project; delivery is at least once and the unavoidable Telegram/local-commit crash window is documented and tested.
- Manual “Fokus Hari Ini” produces the same ranking without modifying scheduled-send idempotency.
- Project archive preserves all history and removes its backlog from active recommendations; archived items follow the documented read/complete-only matrix until the project is restored.
- SQLite data survives container replacement and passes backup/restore verification.
- The image runs non-root, has no published port, and passes healthcheck.
- GitHub Actions tests and scans changes, publishes multi-architecture images to public GHCR, and reports an immutable digest.
- A VPS can deploy using only Docker/Compose, an image reference, a local secret environment file, and a persistent volume.

## 20. Future Migration Triggers

Reconsider PostgreSQL or split workers only when at least one of these becomes real:

- multiple users or shared workspaces;
- more than one application replica;
- concurrent writers from another service;
- a public API or web application;
- workload or reliability requirements no longer fit a local single-writer database.

The repository and service interfaces should keep this migration possible without introducing those costs in version 1.
