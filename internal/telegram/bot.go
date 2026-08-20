package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rchmdndy/telegram-backlog-bot/internal/application"
	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/repository"
)

var ErrChatNotBound = errors.New("authorized chat is not bound")

type TelegramAPI interface {
	Send(tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
}

type Bot struct {
	api     TelegramAPI
	db      *repository.Repositories
	userID  int64
	chatID  atomic.Int64
	bound   atomic.Bool
	limit   int
	loc     *time.Location
	log     *slog.Logger
	Alive   func()
	mu      sync.Mutex
	handler *Handler
}

func New(api TelegramAPI, repos *repository.Repositories, userID, chatID int64, chatIDSet bool, limit int, loc *time.Location, log *slog.Logger) *Bot {
	b := &Bot{api: api, db: repos, userID: userID, limit: limit, loc: loc, log: log}
	b.handler = &Handler{bot: b, db: repos, projectSvc: application.NewProjectService(repos), backlog: application.NewBacklogService(repos), notifier: application.NewNotificationService(repos), userID: userID, limit: limit, loc: loc, log: log}
	if chatIDSet {
		b.chatID.Store(chatID)
		b.bound.Store(true)
	}
	return b
}
func (b *Bot) InitializeBinding(ctx context.Context, explicit bool) error {
	stored, err := b.db.GetAuthorizedChat(ctx, b.userID)
	if err != nil && !repository.IsNotFound(err) {
		return err
	}
	if explicit {
		if err == nil && stored != b.chatID.Load() {
			return repository.ErrAuthorizedChatMismatch
		}
		if err == nil {
			b.bound.Store(true)
			return nil
		}
		if _, err = b.db.BindAuthorizedChat(ctx, b.userID, b.chatID.Load()); err != nil {
			return err
		}
		b.bound.Store(true)
		return nil
	}
	if err == nil {
		b.chatID.Store(stored)
		b.bound.Store(true)
	}
	return nil
}
func (b *Bot) Poll(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := b.api.GetUpdatesChan(u)
	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return nil
		case update, ok := <-updates:
			if b.Alive != nil {
				b.Alive()
			}
			if !ok {

				return nil
			}
			b.mu.Lock()
			err := b.handle(ctx, update)
			b.mu.Unlock()
			if err != nil {
				b.log.Error("update failed", "err", err)
			}
		}
	}
}
func (b *Bot) authorized(user, chat int64) bool {
	return b.bound.Load() && user == b.userID && chat == b.chatID.Load()
}
func (b *Bot) bindStart(ctx context.Context, m *tgbotapi.Message) error {
	if b.bound.Load() || m.From == nil || m.Chat == nil || m.Chat.Type != "private" || m.From.ID != b.userID || strings.TrimSpace(m.Text) != "/start" {
		return nil
	}
	chatID, err := b.db.BindAuthorizedChat(ctx, b.userID, m.Chat.ID)
	if err != nil {
		return err
	}
	b.chatID.Store(chatID)
	b.bound.Store(true)
	return nil
}
func (b *Bot) handle(ctx context.Context, u tgbotapi.Update) error {
	if u.UpdateID == 0 {
		return nil
	}
	if q := u.CallbackQuery; q != nil {
		if q.Message == nil || q.Message.Chat == nil || q.Message.Chat.Type != "private" || !b.authorized(q.From.ID, q.Message.Chat.ID) {
			return nil
		}
		_, _ = b.api.Request(tgbotapi.NewCallback(q.ID, ""))
		if err := b.callback(ctx, q, int64(u.UpdateID)); err != nil {
			return err
		}
		_, err := b.db.MarkProcessed(ctx, int64(u.UpdateID))
		return err
	}

	if u.Message == nil {
		return nil
	}
	if err := b.bindStart(ctx, u.Message); err != nil {
		return err
	}
	if !b.authorized(messageUserID(u.Message), messageChatID(u.Message)) {
		return nil
	}
	processed, err := b.db.IsProcessed(ctx, int64(u.UpdateID))
	if err != nil || processed {
		return err
	}
	if err := b.handler.message(ctx, u.Message, int64(u.UpdateID)); err != nil {
		return err
	}
	_, err = b.db.MarkProcessed(ctx, int64(u.UpdateID))
	return err

}
func messageUserID(m *tgbotapi.Message) int64 {
	if m == nil || m.From == nil {
		return 0
	}
	return m.From.ID
}
func messageChatID(m *tgbotapi.Message) int64 {
	if m == nil || m.Chat == nil {
		return 0
	}
	return m.Chat.ID
}

func (b *Bot) Send(ctx context.Context, text string) (int, error) {
	return b.sendNotification(ctx, text, nil)
}

func (b *Bot) SendNotification(ctx context.Context, text string, keys [][]tgbotapi.InlineKeyboardButton) (int, error) {
	return b.sendNotification(ctx, text, keys)
}

func (b *Bot) sendNotification(ctx context.Context, text string, keys [][]tgbotapi.InlineKeyboardButton) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !b.bound.Load() {
		return 0, ErrChatNotBound
	}
	keys, err := b.opaqueKeys(keys)
	if err != nil {
		return 0, err
	}
	msg := tgbotapi.NewMessage(b.chatID.Load(), text)
	msg.ParseMode = "HTML"
	if len(keys) > 0 {
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(keys...)
	}
	returned, err := b.api.Send(msg)
	return returned.MessageID, err
}

func (b *Bot) send(text string, keys [][]tgbotapi.InlineKeyboardButton) error {
	if !b.bound.Load() {
		return ErrChatNotBound
	}
	keys, err := b.opaqueKeys(keys)
	if err != nil {
		return err
	}
	parts := LimitMessage(text, 4096)
	for n, part := range parts {
		m := tgbotapi.NewMessage(b.chatID.Load(), part)
		m.ParseMode = "HTML"
		if n == len(parts)-1 && len(keys) > 0 {
			m.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(keys...)
		}
		if _, err := b.api.Send(m); err != nil {
			return err
		}
	}
	return nil
}

// opaqueKeys keeps Telegram callback_data below its 64-byte limit while retaining
// the full action, entity id, nonce, and expected version in SQLite.
func (b *Bot) opaqueKeys(keys [][]tgbotapi.InlineKeyboardButton) ([][]tgbotapi.InlineKeyboardButton, error) {
	for row := range keys {
		for col := range keys[row] {
			data := keys[row][col].CallbackData
			if data == nil || !strings.HasPrefix(*data, "v2:") || len(*data) <= 64 {
				continue
			}
			var raw [6]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return nil, err
			}
			token := "t:" + hex.EncodeToString(raw[:])
			if err := b.db.SaveCallbackToken(context.Background(), token, b.userID, *data, time.Now().Add(24*time.Hour)); err != nil {
				return nil, err
			}
			keys[row][col].CallbackData = strPtr(token)
		}
	}
	return keys, nil
}
func menu() [][]tgbotapi.InlineKeyboardButton {
	return [][]tgbotapi.InlineKeyboardButton{
		{tgbotapi.NewInlineKeyboardButtonData("➕ Tambah Backlog", "v2:add")},
		{tgbotapi.NewInlineKeyboardButtonData("📋 Daftar Backlog", "v2:list:0")},
		{tgbotapi.NewInlineKeyboardButtonData("📁 Projects", "v2:projects:0:0")},
		{tgbotapi.NewInlineKeyboardButtonData("☀️ Fokus Hari Ini", "v2:focus")},
		{tgbotapi.NewInlineKeyboardButtonData("❓ Bantuan", "v2:help")},
	}
}
func cancelKeys() [][]tgbotapi.InlineKeyboardButton {
	return [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}}
}

func callbackNonce() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return domain.NewID()
	}
	return hex.EncodeToString(raw[:])
}
func pageValue(s string) int {
	n, _ := strconv.Atoi(s)
	if n < 0 {
		return 0
	}
	return n
}
func (b *Bot) now() time.Time { return time.Now().In(b.loc) }
func strPtr(v string) *string { return &v }

func (b *Bot) callback(ctx context.Context, q *tgbotapi.CallbackQuery, updateID int64) error {
	data := q.Data
	if strings.HasPrefix(data, "t:") {
		resolved, err := b.db.ResolveCallbackToken(ctx, data, b.userID, b.now())
		if err != nil {
			return b.handler.stale(ctx)
		}
		q.Data = resolved
		data = resolved
	}
	if !strings.HasPrefix(data, "v2:") {
		return b.handler.stale(ctx)
	}
	return b.handler.callbackV2(ctx, q, updateID)
}
