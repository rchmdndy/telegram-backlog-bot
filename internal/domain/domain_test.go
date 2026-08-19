package domain

import (
	"testing"
	"time"
)

func TestSortRecommendations(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	items := []RecommendationItem{{Item: BacklogItem{ID: "b", Quadrant: Q1, DeadlineDate: "2026-08-20", CreatedAt: now}}, {Item: BacklogItem{ID: "a", Quadrant: Q2, DeadlineDate: "2026-08-18", CreatedAt: now}}}
	SortRecommendations(items, "2026-08-19")
	if items[0].Item.ID != "a" {
		t.Fatal("overdue item should rank first")
	}
}
func TestParseDeadline(t *testing.T) {
	loc := time.FixedZone("Jakarta", 7*3600)
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, loc)
	got, err := ParseDeadline("20-08-2026", now)
	if err != nil || got != "2026-08-20" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ParseDeadline("2026-08-18", now); err == nil {
		t.Fatal("past date accepted")
	}
}
