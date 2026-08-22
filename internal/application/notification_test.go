package application

import "testing"

func TestNotificationKeyboardSnapshot(t *testing.T) {
	got, err := notificationKeyboard("2026-08-20", false)
	if err != nil {
		t.Fatal(err)
	}
	want := `[[{"text":"✅ Tandai Selesai","callback_data":"v2:notificationitems:2026-08-20"}],[{"text":"📋 Buka Backlog","callback_data":"v2:list:0"}],[{"text":"🏠 Menu Utama","callback_data":"v2:menu"}]]`
	if got != want {
		t.Fatalf("keyboard = %q, want %q", got, want)
	}
}
