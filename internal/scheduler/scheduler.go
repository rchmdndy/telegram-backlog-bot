package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/recommendation"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
)

type Clock interface{ Now() time.Time }
type Sender interface {
	Send(context.Context, string) (int, error)
}
type KeyboardSender interface {
	SendNotification(context.Context, string, [][]tgbotapi.InlineKeyboardButton) (int, error)
}
type RetryAfterError interface{ RetryAfter() time.Duration }
type TelegramRetryAfterError interface{ RetryAfter() int }
type RetryAfter int

func (e RetryAfter) Error() string             { return fmt.Sprintf("retry after %ds", int(e)) }
func (e RetryAfter) RetryAfter() time.Duration { return time.Duration(e) * time.Second }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

type Scheduler struct {
	DB                  *store.Store
	Clock               Clock
	Sender              Sender
	Location            *time.Location
	Hour, Minute, Limit int
	Alive               func()
}

func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if s.Alive != nil {
			s.Alive()
		}
		if err := s.Tick(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context) error {
	now := s.Clock.Now().In(s.Location)
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, s.Location)
	if err := s.DB.RecoverStaleNotifications(ctx, now.UTC().Add(-10*time.Minute)); err != nil {
		return err
	}
	date := now.Format("2006-01-02")
	if err := s.DB.RetryFailedNotifications(ctx, date, now.UTC()); err != nil {
		return err
	}
	scheduled := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, s.Location)
	if now.Before(scheduled) {
		return nil
	}
	if !now.Before(cutoff) {
		return nil
	}
	run, err := s.DB.GetNotificationRun(ctx, date)
	if err != nil && !store.IsNotFound(err) {
		return err
	}
	if run.Status == "sent" {
		return nil
	}
	if err := s.DB.EnsureNotificationRun(ctx, date, scheduled.UTC(), now.UTC()); err != nil {
		return err
	}
	exists, err := s.DB.NotificationSnapshotExists(ctx, date)
	if err != nil {
		return err
	}
	if !exists {
		items, err := s.DB.ListItems(ctx, false, "")
		if err != nil {
			return err
		}
		selected := recommendation.Select(items, now, s.Limit)
		texts := recommendation.RenderParts(selected, now, 4096)
		if len(texts) == 0 {
			texts = []string{recommendation.Render(selected, now)}
		}
		keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("📋 Buka Backlog", "v2:list:0")}}
		if len(selected) == 0 {
			keys = [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("➕ Tambah Backlog", "v2:add")}}
		} else {
			keys = append([][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("✅ Tandai Selesai", "v2:notificationitems:"+date)}}, keys...)
		}
		keyboard, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		parts := make([]store.NotificationPart, len(texts))
		for i, text := range texts {
			parts[i] = store.NotificationPart{Index: i, Payload: text, Keyboard: string(keyboard)}
		}
		if err := s.DB.SnapshotNotification(ctx, date, selected, parts, now.UTC()); err != nil {
			return err
		}
	}
	pending, err := s.DB.PendingNotificationParts(ctx, date)
	if err != nil {
		return err
	}
	for _, part := range pending {
		if !s.Clock.Now().In(s.Location).Before(cutoff) {
			return nil
		}
		messageID, err := s.sendPartWithRetry(ctx, date, part.Payload, part.Keyboard, cutoff)

		if err != nil {
			if failErr := s.DB.FailNotificationRun(ctx, date, err.Error(), s.Clock.Now().UTC()); failErr != nil {
				return failErr
			}
			return nil
		}

		if err := s.DB.MarkNotificationPartSent(ctx, date, part.Index, messageID, s.Clock.Now().UTC()); err != nil {
			return err
		}
	}
	return s.DB.MarkNotificationSent(ctx, date, s.Clock.Now().UTC())
}

func (s *Scheduler) sendPartWithRetry(ctx context.Context, date, payload, keyboard string, cutoff time.Time) (int, error) {
	if sender, ok := s.Sender.(KeyboardSender); ok {
		var keys [][]tgbotapi.InlineKeyboardButton
		if err := json.Unmarshal([]byte(keyboard), &keys); err != nil {
			return 0, err
		}
		return s.sendFuncWithRetry(ctx, cutoff, func() (int, error) { return sender.SendNotification(ctx, payload, keys) })
	}
	return s.sendWithRetry(ctx, payload, cutoff)
}

func (s *Scheduler) sendWithRetry(ctx context.Context, payload string, cutoff time.Time) (int, error) {
	return s.sendFuncWithRetry(ctx, cutoff, func() (int, error) { return s.Sender.Send(ctx, payload) })
}

func (s *Scheduler) sendFuncWithRetry(ctx context.Context, cutoff time.Time, send func() (int, error)) (int, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if !s.Clock.Now().In(s.Location).Before(cutoff) {
			if last == nil {
				last = fmt.Errorf("notification cutoff reached")
			}
			return 0, last
		}
		id, err := send()
		if err == nil {
			return id, nil
		}
		last = err
		if permanentTelegramError(err) {
			return 0, err
		}
		d, retryable := retryDelay(err, attempt)
		if !retryable || d >= cutoff.Sub(s.Clock.Now().In(s.Location)) {
			if retryable {
				return 0, last
			}
			return 0, last
		}
		if !sleepUntil(ctx, d) {
			return 0, ctx.Err()
		}
	}
	if last == nil {
		last = fmt.Errorf("notification retries exhausted")
	}
	return 0, last
}

func permanentTelegramError(err error) bool {
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != 429
	}
	return false
}

func retryDelay(err error, attempt int) (time.Duration, bool) {
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == 429 {
		d := time.Duration(apiErr.RetryAfter) * time.Second
		if d > 30*time.Minute {
			d = 30 * time.Minute
		}
		return d, true
	}
	var apiRetry TelegramRetryAfterError
	if errors.As(err, &apiRetry) {
		d := time.Duration(apiRetry.RetryAfter()) * time.Second
		if d > 30*time.Minute {
			d = 30 * time.Minute
		}
		return d, true
	}
	var retry RetryAfterError
	if errors.As(err, &retry) {
		d := retry.RetryAfter()
		if d > 30*time.Minute {
			d = 30 * time.Minute
		}
		return d, true
	}
	base := time.Duration(1<<min(attempt, 10)) * time.Second
	return base + time.Duration(rand.Int63n(int64(base/4+1))), true
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sleepUntil(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func NextRun(now time.Time, loc *time.Location, hour, minute int) time.Time {
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func ValidateLimit(limit int) error {
	if limit < 1 || limit > 10 {
		return fmt.Errorf("recommendation limit must be 1..10")
	}
	return nil
}

var _ = domain.ItemActive
