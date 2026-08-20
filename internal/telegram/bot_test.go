package telegram

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
)

type fakeAPI struct{ sent []tgbotapi.Chattable }

func (f *fakeAPI) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.sent = append(f.sent, c)
	return tgbotapi.Message{MessageID: 1}, nil
}
func (f *fakeAPI) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) { return nil, nil }
func (f *fakeAPI) GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	return make(chan tgbotapi.Update)
}
func (f *fakeAPI) StopReceivingUpdates() {}

func testBot(t *testing.T, chatID int64, explicit bool) (*Bot, *store.Store, *fakeAPI) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	b := New(api, db, 7, chatID, explicit, 10, time.UTC, slog.Default())
	return b, db, api
}
func message(updateID int, user, chat int64, typ, text string) tgbotapi.Update {
	return tgbotapi.Update{UpdateID: updateID, Message: &tgbotapi.Message{From: &tgbotapi.User{ID: user}, Chat: &tgbotapi.Chat{ID: chat, Type: typ}, Text: text}}
}

func TestBootstrapBindsOnlyPrivateExactStart(t *testing.T) {
	b, db, api := testBot(t, 0, false)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	for i, u := range []tgbotapi.Update{
		message(1, 99, 11, "private", "/start"),
		message(2, 7, -20, "group", "/start"),
		message(3, 7, 12, "private", "hello"),
	} {
		if err := b.handle(ctx, u); err != nil {
			t.Fatal(err)
		}
		if len(api.sent) != 0 {
			t.Fatalf("update %d sent a response", i)
		}
	}
	if err := b.handle(ctx, message(4, 7, 12, "private", "/start")); err != nil {
		t.Fatal(err)
	}
	if len(api.sent) != 1 {
		t.Fatalf("start responses = %d", len(api.sent))
	}
	if got, err := db.GetAuthorizedChat(ctx, 7); err != nil || got != 12 {
		t.Fatalf("binding = %d, %v", got, err)
	}
}

func TestBootstrapRejectsCallbackBeforeBinding(t *testing.T) {
	b, db, api := testBot(t, 0, false)
	defer func() { _ = db.Close() }()
	update := tgbotapi.Update{UpdateID: 1, CallbackQuery: &tgbotapi.CallbackQuery{ID: "q", From: &tgbotapi.User{ID: 7}, Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: 12, Type: "private"}}, Data: "v2:help"}}
	if err := b.handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if len(api.sent) != 0 {
		t.Fatal("callback sent a response before binding")
	}
}

func TestBootstrapPersistsAcrossRestartAndRejectsMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	b := New(api, db, 7, 0, false, 10, time.UTC, slog.Default())
	if err := b.handle(context.Background(), message(1, 7, 12, "private", "/start")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	restarted := New(&fakeAPI{}, db, 7, 0, false, 10, time.UTC, slog.Default())
	if err := restarted.InitializeBinding(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !restarted.authorized(7, 12) || restarted.authorized(7, 13) {
		t.Fatal("restart authorization mismatch")
	}
	explicit := New(&fakeAPI{}, db, 7, 13, true, 10, time.UTC, slog.Default())
	if err := explicit.InitializeBinding(context.Background(), true); err == nil {
		t.Fatal("expected explicit mismatch")
	}
}
