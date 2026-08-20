package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (r *ConversationRepository) GetAuthorizedChat(ctx context.Context, userID int64) (int64, error) {
	var chatID int64
	err := r.db.QueryRowContext(ctx, `SELECT chat_id FROM authorized_chat_binding WHERE authorized_user_id=?`, userID).Scan(&chatID)
	return chatID, err
}
func (r *ConversationRepository) BindAuthorizedChat(ctx context.Context, userID, chatID int64) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err = tx.ExecContext(ctx, `INSERT INTO authorized_chat_binding(authorized_user_id,chat_id,bound_at) VALUES(?,?,?) ON CONFLICT(authorized_user_id) DO NOTHING`, userID, chatID, time.Now().UnixMicro()); err != nil {
		return 0, err
	}
	var bound int64
	if err = tx.QueryRowContext(ctx, `SELECT chat_id FROM authorized_chat_binding WHERE authorized_user_id=?`, userID).Scan(&bound); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	if bound != chatID {
		return bound, ErrAuthorizedChatMismatch
	}
	return bound, nil
}
func (r *ConversationRepository) SaveState(ctx context.Context, user int64, flow, step, draft, nonce string, version int, expires time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO conversation_states(telegram_user_id,flow,step,draft_json,draft_id,draft_version,schema_version,updated_at,expires_at) VALUES(?,?,?,?,?,?,1,?,?) ON CONFLICT(telegram_user_id) DO UPDATE SET flow=excluded.flow,step=excluded.step,draft_json=excluded.draft_json,draft_id=excluded.draft_id,draft_version=excluded.draft_version,schema_version=1,updated_at=excluded.updated_at,expires_at=excluded.expires_at`, user, flow, step, draft, nonce, version, micros(time.Now()), micros(expires))
	return err
}
func (r *ConversationRepository) SaveStateVersion(ctx context.Context, user int64, flow, step, draft, nonce string, expected, next int, expires time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE conversation_states SET flow=?,step=?,draft_json=?,draft_id=?,draft_version=?,schema_version=1,updated_at=?,expires_at=? WHERE telegram_user_id=? AND draft_version=?`, flow, step, draft, nonce, next, micros(time.Now()), micros(expires), user, expected)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (r *ConversationRepository) ClearState(ctx context.Context, user int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM conversation_states WHERE telegram_user_id=?`, user)
	return err
}
func (r *ConversationRepository) ClearStateTx(ctx context.Context, tx *sql.Tx, user int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM conversation_states WHERE telegram_user_id=?`, user)
	return err
}
func (r *ConversationRepository) GetState(ctx context.Context, user int64) (flow, step, draft, nonce string, version int, expires time.Time, err error) {
	var ex int64
	err = r.db.QueryRowContext(ctx, `SELECT flow,step,draft_json,draft_id,draft_version,expires_at FROM conversation_states WHERE telegram_user_id=?`, user).Scan(&flow, &step, &draft, &nonce, &version, &ex)
	expires = fromMicros(ex)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}
func (r *ConversationRepository) SaveCallbackToken(ctx context.Context, token string, user int64, payload string, expires time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO callback_tokens(token,telegram_user_id,payload,created_at,expires_at) VALUES(?,?,?,?,?)`, token, user, payload, micros(time.Now()), micros(expires))
	return err
}
func (r *ConversationRepository) ResolveCallbackToken(ctx context.Context, token string, user int64, now time.Time) (string, error) {
	var payload string
	err := r.db.QueryRowContext(ctx, `SELECT payload FROM callback_tokens WHERE token=? AND telegram_user_id=? AND expires_at>=?`, token, user, micros(now)).Scan(&payload)
	if err != nil {
		return "", err
	}
	return payload, nil
}
func (r *ConversationRepository) IsProcessed(ctx context.Context, id int64) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_updates WHERE update_id=?`, id).Scan(&n)
	return n != 0, err
}
func (r *ConversationRepository) MarkProcessed(ctx context.Context, id int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO processed_updates(update_id,processed_at) VALUES(?,?)`, id, micros(time.Now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (r *ConversationRepository) Receipt(ctx context.Context, nonce string) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT result_json FROM mutation_receipts WHERE nonce=?`, nonce).Scan(&v)
	return v, err
}
func (r *ConversationRepository) SaveReceipt(ctx context.Context, nonce, action, id, result string) error {
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO mutation_receipts(nonce,action,entity_id,result_json,processed_at) VALUES(?,?,?,?,?)`, nonce, action, id, result, micros(time.Now()))
	return err
}
