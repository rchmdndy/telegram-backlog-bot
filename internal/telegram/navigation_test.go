package telegram

import (
	"context"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestHomeKeepsDraftAndSkipsTextInput(t *testing.T) {
	b, db, api := testBot(t, 12, true)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if err := b.handler.startBacklog(ctx); err != nil {
		t.Fatal(err)
	}
	flow, step, raw, nonce, version, expires, err := b.db.GetState(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.handler.callbackV2(ctx, &tgbotapi.CallbackQuery{Data: "v2:menu"}, 1); err != nil {
		t.Fatal(err)
	}
	gotFlow, gotStep, gotRaw, gotNonce, gotVersion, gotExpires, err := b.db.GetState(ctx, 7)
	if err != nil || gotFlow != flow || gotStep != step || gotRaw != raw || gotNonce != nonce || gotVersion != version || !gotExpires.Equal(expires) {
		t.Fatalf("home changed draft: %q/%q/%q/%q/%d/%v, err=%v", gotFlow, gotStep, gotRaw, gotNonce, gotVersion, gotExpires, err)
	}
	if len(api.sent) < 2 {
		t.Fatalf("sent = %d, want at least 2", len(api.sent))
	}

	if !textInputStep("backlog", "title") || !textInputStep("backlog", "date") || textInputStep("backlog", "project") {
		t.Fatal("unexpected text-input state classification")
	}
	keys := withHome(nil)
	if len(keys) != 1 || len(keys[0]) != 1 || keys[0][0].CallbackData == nil || *keys[0][0].CallbackData != "v2:menu" {
		t.Fatalf("home keys = %#v", keys)
	}
}
