// Package events persists presence events to disk so the system can answer
// retrospective queries — "what happened in the last hour", "show me the 3pm
// one". Storage is a single JSON-lines file with one record per event, plus
// a flat directory of JPEGs named by event ID. No database; volumes are
// small (≈50 events/day, 7-day retention ≈ 350 events).
//
// Writers (gatemanai) must be a single process; the janitor relies on that
// to avoid a write-while-rewrite race. Readers (mcpserver) are read-only
// and safe under concurrent append because each event is one short line.
package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one record in the on-disk log.
type Event struct {
	ID          string    `json:"id"`          // sortable timestamp form, e.g. "20260513-150405"
	Timestamp   time.Time `json:"ts"`          // RFC3339 in JSON
	Description string    `json:"description"` // Gemma's scene description (may be empty if vision failed)
	PhotoPath   string    `json:"photo"`       // absolute path to the JPEG file
}

// Store is the persistence interface — mockable for tests.
type Store interface {
	Append(e Event) error
	Recent(window time.Duration) ([]Event, error)
	Get(id string) (Event, error)
	Purge(maxAge time.Duration) (removed int, err error)
}

// JSONLStore stores events in <root>/events.jsonl and photos in <root>/photos/.
type JSONLStore struct {
	root string
	mu   sync.Mutex
}

// NewJSONLStore creates the storage layout under root (mkdir -p both files
// and the photos directory). Safe to call on every process start; if the
// directories already exist nothing happens.
func NewJSONLStore(root string) (*JSONLStore, error) {
	if root == "" {
		return nil, fmt.Errorf("events: empty root path")
	}
	if err := os.MkdirAll(filepath.Join(root, "photos"), 0755); err != nil {
		return nil, fmt.Errorf("events: mkdir: %w", err)
	}
	return &JSONLStore{root: root}, nil
}

// Root returns the storage root directory.
func (s *JSONLStore) Root() string { return s.root }

// PhotoPath returns the canonical path where a photo for the given event ID
// should be stored. Callers write to this path themselves; the store doesn't
// own photo bytes.
func (s *JSONLStore) PhotoPath(id string) string {
	return filepath.Join(s.root, "photos", id+".jpg")
}

func (s *JSONLStore) logPath() string { return filepath.Join(s.root, "events.jsonl") }

// NewID returns a sortable ID derived from a timestamp. Lexicographic sort
// matches chronological order.
func NewID(t time.Time) string {
	return t.UTC().Format("20060102-150405")
}

// Append writes a single event line to the log. Single-writer safe.
func (s *JSONLStore) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("events: marshal: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(s.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("events: open log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("events: append: %w", err)
	}
	return nil
}

// readAll scans the log file and returns every parseable line. Bad lines
// are skipped silently — a partially-flushed final line never breaks reads.
func (s *JSONLStore) readAll() ([]Event, error) {
	f, err := os.Open(s.logPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("events: open log: %w", err)
	}
	defer f.Close()

	var out []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip malformed
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}

// Recent returns events from the last `window` duration, newest first.
func (s *JSONLStore) Recent(window time.Duration) ([]Event, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-window)
	var out []Event
	for _, e := range all {
		if e.Timestamp.After(cutoff) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out, nil
}

// Get returns the event with the given ID, or an error if not found.
func (s *JSONLStore) Get(id string) (Event, error) {
	all, err := s.readAll()
	if err != nil {
		return Event{}, err
	}
	for _, e := range all {
		if e.ID == id {
			return e, nil
		}
	}
	return Event{}, fmt.Errorf("events: id %q not found", id)
}

// Purge rewrites the log to drop events older than maxAge, and removes the
// orphan JPEGs that go with them. Returns the number of records removed.
// Single-writer safe (call from gatemanai only).
func (s *JSONLStore) Purge(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.readAll()
	if err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-maxAge)
	keep := make([]Event, 0, len(all))
	dropped := make([]Event, 0)
	for _, e := range all {
		if e.Timestamp.After(cutoff) {
			keep = append(keep, e)
		} else {
			dropped = append(dropped, e)
		}
	}

	if len(dropped) == 0 {
		return 0, nil
	}

	// Write kept entries to a temp file, then atomic-rename over the original.
	tmp, err := os.CreateTemp(s.root, "events-*.jsonl.tmp")
	if err != nil {
		return 0, fmt.Errorf("events: create temp: %w", err)
	}
	tmpName := tmp.Name()
	w := bufio.NewWriter(tmp)
	for _, e := range keep {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return 0, fmt.Errorf("events: marshal during purge: %w", err)
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return 0, fmt.Errorf("events: flush during purge: %w", err)
	}
	tmp.Close()
	if err := os.Rename(tmpName, s.logPath()); err != nil {
		os.Remove(tmpName)
		return 0, fmt.Errorf("events: rename during purge: %w", err)
	}

	// Best-effort photo cleanup; log errors but don't fail the purge.
	for _, e := range dropped {
		if e.PhotoPath == "" {
			continue
		}
		_ = os.Remove(e.PhotoPath)
	}

	return len(dropped), nil
}

// DefaultRoot returns ~/.gatemanai (or the equivalent on the host). The
// caller is responsible for handling errors from os.UserHomeDir.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gatemanai"), nil
}
