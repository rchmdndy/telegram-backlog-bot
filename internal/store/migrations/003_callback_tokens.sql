CREATE TABLE IF NOT EXISTS callback_tokens (
 token TEXT PRIMARY KEY, telegram_user_id INTEGER NOT NULL, payload TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS callback_tokens_expiry_idx ON callback_tokens(expires_at);
