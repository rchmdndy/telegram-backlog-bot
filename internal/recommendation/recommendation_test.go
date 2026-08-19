package recommendation

import (
	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestSelectLimitAndRenderEscapes(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.FixedZone("Jakarta", 7*3600))
	items := make([]domain.RecommendationItem, 11)
	for i := range items {
		items[i].Item.ID = string(rune('a' + i))
		items[i].Item.Title = "<unsafe>"
		items[i].Item.ProjectID = "p"
		items[i].ProjectName = "A & B"
		items[i].Item.Quadrant = domain.Q1
		items[i].Item.DeadlineDate = "2026-08-19"
		items[i].Item.CreatedAt = now
	}
	got := Select(items, now, 10)
	if len(got) != 10 {
		t.Fatalf("got %d", len(got))
	}
	text := Render(got, now)
	if strings.Contains(text, "<unsafe>") {
		t.Fatal("title was not escaped")
	}
	if !strings.Contains(text, "&lt;unsafe&gt;") {
		t.Fatal("escaped title missing")
	}
}
