CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS projects (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, normalized_name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL CHECK(status IN ('active','archived')),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER,
 CHECK ((status='active' AND archived_at IS NULL) OR (status='archived' AND archived_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS projects_active_name_idx ON projects(normalized_name) WHERE status='active';
CREATE TABLE IF NOT EXISTS backlog_items (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), title TEXT NOT NULL, notes TEXT NOT NULL DEFAULT '', quadrant TEXT NOT NULL CHECK(quadrant IN ('q1','q2','q3','q4')), deadline_date TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('active','done')), created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, completed_at INTEGER,
 CHECK ((status='active' AND completed_at IS NULL) OR (status='done' AND completed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS backlog_active_idx ON backlog_items(status, deadline_date, quadrant);
CREATE TABLE IF NOT EXISTS conversation_states (
 telegram_user_id INTEGER PRIMARY KEY, flow TEXT NOT NULL, step TEXT NOT NULL, draft_json TEXT NOT NULL, draft_id TEXT NOT NULL, draft_version INTEGER NOT NULL, schema_version INTEGER NOT NULL, updated_at INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS notification_runs (
 local_date TEXT PRIMARY KEY, scheduled_for INTEGER NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','sending','sent','failed')), attempt_count INTEGER NOT NULL DEFAULT 0, last_error TEXT, sent_at INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS notification_run_items (
 local_date TEXT NOT NULL REFERENCES notification_runs(local_date), ordinal INTEGER NOT NULL, backlog_item_id TEXT, project_name TEXT NOT NULL, title TEXT NOT NULL, quadrant TEXT NOT NULL, deadline_date TEXT NOT NULL, PRIMARY KEY(local_date, ordinal)
);
CREATE TABLE IF NOT EXISTS notification_parts (
 local_date TEXT NOT NULL REFERENCES notification_runs(local_date), part_index INTEGER NOT NULL, payload_json TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('pending','sent')), telegram_message_id INTEGER, sent_at INTEGER, PRIMARY KEY(local_date, part_index)
);
CREATE TABLE IF NOT EXISTS processed_updates (update_id INTEGER PRIMARY KEY, processed_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS mutation_receipts (nonce TEXT PRIMARY KEY, action TEXT NOT NULL, entity_id TEXT NOT NULL, result_json TEXT NOT NULL, processed_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS processed_updates_time_idx ON processed_updates(processed_at);
CREATE INDEX IF NOT EXISTS mutation_receipts_time_idx ON mutation_receipts(processed_at);
