package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/recommendation"
	"github.com/rchmdndy/telegram-backlog-bot/internal/repository"
)

// NotificationService coordinates persisted notification runs and snapshots.
type NotificationService struct {
	DB *repository.Repositories
}

// NotificationWork is the persisted work for one local notification date.
type NotificationWork struct {
	Status string
	Parts  []repository.NotificationPart
}

// NewNotificationService creates a notification workflow service.
func NewNotificationService(db *repository.Repositories) *NotificationService {
	return &NotificationService{DB: db}
}

// Items returns the persisted items for a notification snapshot.
func (s *NotificationService) Items(ctx context.Context, date string) ([]repository.NotificationItem, error) {
	return s.DB.NotificationItems(ctx, date)
}

// Maintain recovers stale runs and promotes due failed runs for retry.
func (s *NotificationService) Maintain(ctx context.Context, date string, now time.Time) error {
	if err := s.DB.RecoverStaleNotifications(ctx, now.UTC().Add(-10*time.Minute)); err != nil {
		return err
	}
	return s.DB.RetryFailedNotifications(ctx, date, now.UTC())
}

// Prepare creates the daily snapshot once and returns its pending delivery parts.
func (s *NotificationService) Prepare(ctx context.Context, date string, scheduled, now time.Time, limit int) (NotificationWork, error) {
	var work NotificationWork
	run, err := s.DB.GetNotificationRun(ctx, date)
	if err != nil && !repository.IsNotFound(err) {
		return work, err
	}
	if run.Status == "sent" {
		work.Status = run.Status
		return work, nil
	}
	if err := s.DB.EnsureNotificationRun(ctx, date, scheduled.UTC(), now.UTC()); err != nil {
		return work, err
	}
	work.Status = run.Status
	if work.Status == "" {
		work.Status = "pending"
	}

	exists, err := s.DB.NotificationSnapshotExists(ctx, date)
	if err != nil {
		return NotificationWork{}, err
	}
	if !exists {
		items, err := s.DB.ListItems(ctx, false, "")
		if err != nil {
			return NotificationWork{}, err
		}
		selected := recommendation.Select(items, now, limit)
		texts := recommendation.RenderParts(selected, now, 4096)
		if len(texts) == 0 {
			texts = []string{recommendation.Render(selected, now)}
		}
		keyboard, err := notificationKeyboard(date, len(selected) == 0)
		if err != nil {
			return NotificationWork{}, err
		}
		parts := make([]repository.NotificationPart, len(texts))
		for i, text := range texts {
			parts[i] = repository.NotificationPart{Index: i, Payload: text, Keyboard: keyboard}
		}
		if err := s.DB.SnapshotNotification(ctx, date, selected, parts, now.UTC()); err != nil {
			return NotificationWork{}, err
		}
	}
	work.Parts, err = s.DB.PendingNotificationParts(ctx, date)
	return work, err
}

type notificationButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func notificationKeyboard(date string, empty bool) (string, error) {
	keys := [][]notificationButton{{{Text: "📋 Buka Backlog", CallbackData: "v2:list:0"}}}
	if empty {
		keys = [][]notificationButton{{{Text: "➕ Tambah Backlog", CallbackData: "v2:add"}}}
	} else {
		keys = append([][]notificationButton{{{Text: "✅ Tandai Selesai", CallbackData: "v2:notificationitems:" + date}}}, keys...)
	}
	encoded, err := json.Marshal(keys)
	return string(encoded), err
}

// MarkPartSent records one successful delivery attempt.
func (s *NotificationService) MarkPartSent(ctx context.Context, date string, index, messageID int, now time.Time) error {
	return s.DB.MarkNotificationPartSent(ctx, date, index, messageID, now.UTC())
}

// MarkSent marks a run sent after all parts have been recorded as delivered.
func (s *NotificationService) MarkSent(ctx context.Context, date string, now time.Time) error {
	return s.DB.MarkNotificationSent(ctx, date, now.UTC())
}

// Fail records a delivery failure and schedules its persisted retry.
func (s *NotificationService) Fail(ctx context.Context, date, reason string, now time.Time) error {
	return s.DB.FailNotificationRun(ctx, date, reason, now.UTC())
}
