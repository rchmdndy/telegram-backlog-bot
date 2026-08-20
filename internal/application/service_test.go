package application

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/repository"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
)

func TestBacklogRecommendMapsAndOrdersItems(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repos := repository.New(db.DB)
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	project := domain.Project{ID: "project", Name: "Project", NormalizedName: "project", Status: domain.ProjectActive, CreatedAt: now, UpdatedAt: now}
	if err := repos.CreateProject(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	item := domain.BacklogItem{ID: "item", ProjectID: project.ID, Title: "Task", Quadrant: domain.Q1, DeadlineDate: now.Format("2006-01-02"), Status: domain.ItemActive, CreatedAt: now, UpdatedAt: now}
	if err := repos.CreateItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	got, err := NewBacklogService(repos).Recommend(context.Background(), now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Item.ID != item.ID || got[0].ProjectName != project.Name || got[0].Ordinal != 1 {
		t.Fatalf("recommendation = %#v", got)
	}
}

func TestMutationReceiptIsAtomicAndIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	calls := 0
	fn := func(_ *sql.Tx) (string, error) { calls++; return `{"ok":"yes"}`, nil }
	if _, replay, err := repository.NewMutationRepository(db.DB).Mutate(context.Background(), 1, "nonce", "test", "id", fn); err != nil || replay {
		t.Fatalf("first mutation: replay=%v err=%v", replay, err)
	}
	if _, replay, err := repository.NewMutationRepository(db.DB).Mutate(context.Background(), 1, "nonce", "test", "id", fn); err != nil || !replay {
		t.Fatalf("replay: replay=%v err=%v", replay, err)
	}
	if calls != 1 {
		t.Fatalf("mutation called %d times", calls)
	}
}
