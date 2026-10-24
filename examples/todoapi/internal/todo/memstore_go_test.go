package todo

import (
	"context"
	"errors"
	"testing"
	"time"
)

// This test is plain Go. Lyth declarations are ordinary Go declarations, so
// Go code (including tests) uses them directly.
func TestMemStoreFromGo(t *testing.T) {
	now := time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)
	s := NewMemStore(func() time.Time { return now })
	title := "written in Go"
	created, err := s.Create(context.Background(), "gopher", NewTodo{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if created.Title != title || !created.CreatedAt.Equal(now) {
		t.Fatalf("created %+v", created)
	}

	var store Store = s // *MemStore implements the Lyth interface
	if _, err := store.Get(context.Background(), "someone-else", created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get by another owner: err = %v, want ErrNotFound", err)
	}
}
