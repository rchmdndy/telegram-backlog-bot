package scheduler

import (
	"testing"
	"time"
)

func TestNextRunUsesConfiguredLocation(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 5, 59, 0, 0, loc)
	got := NextRun(now, loc, 6, 0)
	if got.Hour() != 6 || got.Location() != loc {
		t.Fatalf("got %v", got)
	}
	if err := ValidateLimit(11); err == nil {
		t.Fatal("invalid limit accepted")
	}
}
