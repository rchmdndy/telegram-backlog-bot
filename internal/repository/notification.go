package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
)

type NotificationRun struct {
	LocalDate, Status       string
	ScheduledFor, UpdatedAt time.Time
	AttemptCount            int
}
type NotificationPart struct {
	Index    int
	Payload  string
	Keyboard string
}
type NotificationItem struct {
	Ordinal       int
	BacklogItemID string
	ProjectName   string
	Title         string
	Quadrant      string
	DeadlineDate  string
}

const notificationRetryWindow = 15 * time.Minute

func (r *NotificationRepository) RecoverStaleNotifications(ctx context.Context, before time.Time) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE notification_runs SET status='failed', last_error='stale sending recovery', attempt_count=attempt_count+1, retry_at=CASE WHEN attempt_count+1 < 4 THEN ? ELSE NULL END, updated_at=? WHERE status='sending' AND updated_at < ?`, micros(now.Add(notificationRetryWindow)), micros(now), micros(before))
	return err
}
func (r *NotificationRepository) RetryFailedNotifications(ctx context.Context, date string, now time.Time) error {
	now = now.UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE notification_runs SET status='sending', retry_at=NULL, updated_at=? WHERE local_date=? AND status='failed' AND attempt_count < 4 AND retry_at IS NOT NULL AND retry_at <= ?`, micros(now), date, micros(now))
	return err
}
func (r *NotificationRepository) GetNotificationRun(ctx context.Context, date string) (NotificationRun, error) {
	var run NotificationRun
	var scheduled, updated int64
	err := r.db.QueryRowContext(ctx, `SELECT local_date,status,scheduled_for,attempt_count,updated_at FROM notification_runs WHERE local_date=?`, date).Scan(&run.LocalDate, &run.Status, &scheduled, &run.AttemptCount, &updated)
	run.ScheduledFor, run.UpdatedAt = fromMicros(scheduled), fromMicros(updated)
	return run, err
}
func (r *NotificationRepository) EnsureNotificationRun(ctx context.Context, date string, scheduled, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO notification_runs(local_date,scheduled_for,status,attempt_count,created_at,updated_at) VALUES(?,?, 'pending',0,?,?) ON CONFLICT(local_date) DO NOTHING`, date, micros(scheduled), micros(now), micros(now))
	return err
}
func (r *NotificationRepository) SnapshotNotification(ctx context.Context, date string, items []domain.RecommendationItem, parts []NotificationPart, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM notification_runs WHERE local_date=?`, date).Scan(&status); err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_run_items(local_date,ordinal,backlog_item_id,project_name,title,quadrant,deadline_date) VALUES(?,?,?,?,?,?,?)`, date, item.Ordinal, item.Item.ID, item.ProjectName, item.Item.Title, item.Item.Quadrant, item.Item.DeadlineDate); err != nil {
			return err
		}
	}
	for _, part := range parts {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_parts(local_date,part_index,payload_json,keyboard_json,status) VALUES(?,?,?,?,'pending')`, date, part.Index, part.Payload, part.Keyboard); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE notification_runs SET status='sending',updated_at=? WHERE local_date=? AND status='pending'`, micros(now), date)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (r *NotificationRepository) NotificationSnapshotExists(ctx context.Context, date string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_runs WHERE local_date=? AND EXISTS (SELECT 1 FROM notification_parts WHERE local_date=?)`, date, date).Scan(&n)
	return n > 0, err
}
func (r *NotificationRepository) NotificationItems(ctx context.Context, date string) ([]NotificationItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT ordinal,backlog_item_id,project_name,title,quadrant,deadline_date FROM notification_run_items WHERE local_date=? ORDER BY ordinal`, date)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []NotificationItem
	for rows.Next() {
		var item NotificationItem
		if err := rows.Scan(&item.Ordinal, &item.BacklogItemID, &item.ProjectName, &item.Title, &item.Quadrant, &item.DeadlineDate); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (r *NotificationRepository) PendingNotificationParts(ctx context.Context, date string) ([]NotificationPart, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT part_index,payload_json,keyboard_json FROM notification_parts WHERE local_date=? AND status='pending' ORDER BY part_index`, date)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []NotificationPart
	for rows.Next() {
		var p NotificationPart
		if err := rows.Scan(&p.Index, &p.Payload, &p.Keyboard); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (r *NotificationRepository) MarkNotificationPartSent(ctx context.Context, date string, index, messageID int, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE notification_parts SET status='sent',telegram_message_id=?,sent_at=? WHERE local_date=? AND part_index=? AND status='pending'`, messageID, micros(now), date, index)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("notification part %d is not pending", index)
	}
	return nil
}
func (r *NotificationRepository) MarkNotificationSent(ctx context.Context, date string, now time.Time) error {
	var pending int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_parts WHERE local_date=? AND status='pending'`, date).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("notification still has pending parts")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE notification_runs SET status='sent',sent_at=?,updated_at=? WHERE local_date=? AND status IN ('pending','sending')`, micros(now), micros(now), date)
	return err
}
func (r *NotificationRepository) FailNotificationRun(ctx context.Context, date, reason string, now time.Time) error {
	now = now.UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE notification_runs SET status='failed',last_error=?,attempt_count=attempt_count+1,retry_at=CASE WHEN attempt_count+1 < 4 THEN ? ELSE NULL END,updated_at=? WHERE local_date=? AND status IN ('sending','pending')`, reason, micros(now.Add(notificationRetryWindow)), micros(now), date)
	return err
}
