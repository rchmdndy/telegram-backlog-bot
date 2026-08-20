CREATE TABLE IF NOT EXISTS authorized_chat_binding (
    authorized_user_id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    bound_at INTEGER NOT NULL
);
