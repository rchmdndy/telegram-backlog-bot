package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestAuthorizedChatBindingPersistenceAndMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binding.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := s.GetAuthorizedChat(ctx, 7); !IsNotFound(err) || got != 0 {
		t.Fatalf("initial binding = %d, %v", got, err)
	}
	if got, err := s.BindAuthorizedChat(ctx, 7, 99); err != nil || got != 99 {
		t.Fatalf("bind = %d, %v", got, err)
	}
	if got, err := s.BindAuthorizedChat(ctx, 7, 99); err != nil || got != 99 {
		t.Fatalf("repeat bind = %d, %v", got, err)
	}
	if got, err := s.BindAuthorizedChat(ctx, 7, 100); !errors.Is(err, ErrAuthorizedChatMismatch) || got != 99 {
		t.Fatalf("mismatch = %d, %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got, err := s.GetAuthorizedChat(ctx, 7); err != nil || got != 99 {
		t.Fatalf("persisted binding = %d, %v", got, err)
	}
}

func TestAuthorizedChatBindingConcurrentFirstBind(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "binding.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, chatID := range []int64{99, 100} {
		wg.Add(1)
		go func(chatID int64) {
			defer wg.Done()
			_, err := s.BindAuthorizedChat(context.Background(), 7, chatID)
			if err != nil && !errors.Is(err, ErrAuthorizedChatMismatch) {
				errs <- err
			}
		}(chatID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got, err := s.GetAuthorizedChat(context.Background(), 7)
	if err != nil || (got != 99 && got != 100) {
		t.Fatalf("concurrent binding = %d, %v", got, err)
	}
}
