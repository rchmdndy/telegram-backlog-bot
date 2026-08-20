package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (r *MutationRepository) Mutate(ctx context.Context, updateID int64, nonce, action, entityID string, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	return r.mutate(ctx, updateID, nonce, action, entityID, 0, false, fn)
}
func (r *MutationRepository) MutateAndClearState(ctx context.Context, updateID int64, nonce, action, entityID string, userID int64, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	return r.mutate(ctx, updateID, nonce, action, entityID, userID, true, fn)
}
func (r *MutationRepository) mutate(ctx context.Context, updateID int64, nonce, action, entityID string, userID int64, clearState bool, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	var existing string
	var existingAction, existingEntity string
	err = tx.QueryRowContext(ctx, `SELECT action,entity_id,result_json FROM mutation_receipts WHERE nonce=?`, nonce).Scan(&existingAction, &existingEntity, &existing)
	if err == nil {
		_ = tx.Rollback()
		if existingAction != action || existingEntity != entityID {
			return "", false, ErrReceiptConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return "", false, err
	}
	var processed int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_updates WHERE update_id=?`, updateID).Scan(&processed); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	if processed != 0 {
		_ = tx.Rollback()
		return "", true, nil
	}
	result, err := fn(tx)
	if err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	now := micros(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO mutation_receipts(nonce,action,entity_id,result_json,processed_at) VALUES(?,?,?,?,?)`, nonce, action, entityID, result, now); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO processed_updates(update_id,processed_at) VALUES(?,?)`, updateID, now); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	if clearState {
		if _, err = tx.ExecContext(ctx, `DELETE FROM conversation_states WHERE telegram_user_id=?`, userID); err != nil {
			_ = tx.Rollback()
			return "", false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", false, err
	}
	return result, false, nil
}
