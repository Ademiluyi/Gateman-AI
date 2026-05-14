package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *JSONLStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	return s
}

func TestAppendAndRecent(t *testing.T) {
	s := newStore(t)
	now := time.Now()

	for i := 0; i < 3; i++ {
		ts := now.Add(time.Duration(-i) * time.Minute)
		err := s.Append(Event{
			ID:          NewID(ts),
			Timestamp:   ts,
			Description: "test event",
			PhotoPath:   "/tmp/fake.jpg",
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	got, err := s.Recent(10 * time.Minute)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	// Newest first.
	if got[0].Timestamp.Before(got[1].Timestamp) {
		t.Errorf("expected newest-first ordering")
	}
}

func TestRecentWindowFiltering(t *testing.T) {
	s := newStore(t)
	now := time.Now()

	// Two events in window, two outside.
	ages := []time.Duration{30 * time.Second, 5 * time.Minute, 2 * time.Hour, 25 * time.Hour}
	for _, age := range ages {
		ts := now.Add(-age)
		if err := s.Append(Event{ID: NewID(ts), Timestamp: ts}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.Recent(time.Hour)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events in last hour, got %d", len(got))
	}
}

func TestGet(t *testing.T) {
	s := newStore(t)
	ts := time.Now()
	id := NewID(ts)
	want := Event{ID: id, Timestamp: ts, Description: "hi"}
	if err := s.Append(want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != "hi" {
		t.Errorf("description mismatch: got %q", got.Description)
	}

	if _, err := s.Get("nonexistent"); err == nil {
		t.Errorf("expected error for missing id")
	}
}

func TestPurgeDropsOldEntries(t *testing.T) {
	s := newStore(t)
	now := time.Now()

	old := now.Add(-2 * time.Hour)
	newish := now.Add(-5 * time.Minute)

	// Create real photo files so the purge can clean them up.
	oldPhoto := filepath.Join(s.root, "photos", NewID(old)+".jpg")
	newPhoto := filepath.Join(s.root, "photos", NewID(newish)+".jpg")
	if err := os.WriteFile(oldPhoto, []byte("fake-jpeg"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPhoto, []byte("fake-jpeg"), 0644); err != nil {
		t.Fatal(err)
	}

	s.Append(Event{ID: NewID(old), Timestamp: old, PhotoPath: oldPhoto})
	s.Append(Event{ID: NewID(newish), Timestamp: newish, PhotoPath: newPhoto})

	removed, err := s.Purge(time.Hour)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	// Old photo should be gone; new photo should remain.
	if _, err := os.Stat(oldPhoto); !os.IsNotExist(err) {
		t.Errorf("expected old photo deleted, got %v", err)
	}
	if _, err := os.Stat(newPhoto); err != nil {
		t.Errorf("expected new photo kept, got %v", err)
	}

	// Reading after purge returns only the new event.
	got, err := s.Recent(48 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 event after purge, got %d", len(got))
	}
}

func TestPurgeNoopWhenNothingExpired(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	s.Append(Event{ID: NewID(now), Timestamp: now})

	removed, err := s.Purge(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("expected no removals, got %d", removed)
	}
}

func TestReadHandlesMissingFile(t *testing.T) {
	s := newStore(t)
	got, err := s.Recent(time.Hour)
	if err != nil {
		t.Fatalf("Recent on empty store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 events, got %d", len(got))
	}
}

func TestReadSkipsCorruptedLines(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	s.Append(Event{ID: NewID(now), Timestamp: now, Description: "good"})
	// Append a malformed line directly.
	f, _ := os.OpenFile(s.logPath(), os.O_APPEND|os.O_WRONLY, 0644)
	f.Write([]byte("this is not json\n"))
	f.Close()
	s.Append(Event{ID: NewID(now.Add(time.Second)), Timestamp: now.Add(time.Second), Description: "also good"})

	got, err := s.Recent(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 valid events (corrupt line skipped), got %d", len(got))
	}
}
