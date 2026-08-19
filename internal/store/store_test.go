package store

import (
	"context"
	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCRUDAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backlog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	p := domain.Project{ID: domain.NewID(), Name: "Work", NormalizedName: domain.NormalizeProjectName("Work"), Status: domain.ProjectActive, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListProjects(ctx, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("projects: %v %d", err, len(items))
	}
	i := domain.BacklogItem{ID: domain.NewID(), ProjectID: p.ID, Title: "Ship", Quadrant: domain.Q1, DeadlineDate: now.Format("2006-01-02"), Status: domain.ItemActive, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateItem(ctx, i); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListItems(ctx, false, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("items: %v %d", err, len(got))
	}
	if ok, err := s.MarkProcessed(ctx, 42); err != nil || !ok {
		t.Fatalf("processed: %v %v", ok, err)
	}
	if ok, err := s.MarkProcessed(ctx, 42); err != nil || ok {
		t.Fatalf("duplicate processed: %v %v", ok, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Integrity(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreIntegration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := domain.Project{ID: domain.NewID(), Name: "Restore", NormalizedName: domain.NormalizeProjectName("Restore"), Status: domain.ProjectActive, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	if err := s.Backup(ctx, backupDir); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(backupDir, "backlog-*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("backup files: %v %v", files, err)
	}
	restored, err := Open(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(restored.Path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restored.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err = Open(restored.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.GetProject(ctx, p.ID)
	if err != nil || got.Name != p.Name {
		t.Fatalf("restored project: %v %v", got, err)
	}
}
