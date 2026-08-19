package application

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
)

func TestProjectAndBacklogLifecycle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.FixedZone("Asia/Jakarta", 7*3600))
	projects := NewProjectService(db)
	backlog := NewBacklogService(db)
	p, err := projects.Create(ctx, " Work ", "desc", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = projects.Create(ctx, "work", "duplicate", now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate project: %v", err)
	}
	i, err := backlog.Create(ctx, p.ID, "Ship", "", domain.Q1, "2026-08-20", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = projects.Archive(ctx, p.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = backlog.Complete(ctx, i.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = backlog.Reopen(ctx, i.ID, now); !errors.Is(err, domain.ErrArchived) {
		t.Fatalf("reopen archived: %v", err)
	}
	if err = projects.Restore(ctx, p.ID, now); err != nil {
		t.Fatal(err)
	}
	if err = backlog.Reopen(ctx, i.ID, now); err != nil {
		t.Fatal(err)
	}
}

func TestMutationReceiptIsAtomicAndIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	calls := 0
	fn := func(_ *sql.Tx) (any, error) { calls++; return map[string]string{"ok": "yes"}, nil }
	if _, replay, err := Mutate(ctx, db, 1, "nonce", "test", "id", fn); err != nil || replay {
		t.Fatalf("first mutation: replay=%v err=%v", replay, err)
	}
	if _, replay, err := Mutate(ctx, db, 1, "nonce", "test", "id", fn); err != nil || !replay {
		t.Fatalf("replay: replay=%v err=%v", replay, err)
	}
	if calls != 1 {
		t.Fatalf("mutation called %d times", calls)
	}
}
