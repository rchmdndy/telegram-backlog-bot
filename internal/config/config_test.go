package config

import (
	"strings"
	"testing"
)

func TestLoadAuthorizedChatOptional(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_AUTHORIZED_USER_ID", "42")
	t.Setenv("TELEGRAM_AUTHORIZED_CHAT_ID", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthorizedUserID != 42 || c.AuthorizedChatIDSet || c.AuthorizedChatID != 0 {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadAuthorizedChatValidationAndUserRequired(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_AUTHORIZED_USER_ID", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TELEGRAM_AUTHORIZED_USER_ID is required") {
		t.Fatalf("missing user error = %v", err)
	}
	t.Setenv("TELEGRAM_AUTHORIZED_CHAT_ID", "not-an-int")
	t.Setenv("TELEGRAM_AUTHORIZED_USER_ID", "42")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TELEGRAM_AUTHORIZED_CHAT_ID") {
		t.Fatalf("invalid chat error = %v", err)
	}
}
