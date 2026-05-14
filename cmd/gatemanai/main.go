package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/events"
	"github.com/ade/gatemanai/internal/openclaw"
	"github.com/ade/gatemanai/internal/presence"
	"github.com/ade/gatemanai/internal/retry"
	"github.com/ade/gatemanai/internal/vision"
)

// Retry budgets tuned for home-Wi-Fi reality: a Pi briefly unreachable during
// a router reboot, OpenClaw mid-reconnect, or Ollama swapping under memory
// pressure. Total time budgets (including retries + backoff) are bounded by the
// per-call context timeouts in handleRing.
var (
	captureRetry = retry.Config{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    4 * time.Second,
		Label:       "capture",
	}
	sendRetry = retry.Config{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    8 * time.Second,
		Label:       "openclaw send",
	}
	visionRetry = retry.Config{
		MaxAttempts: 2,
		BaseDelay:   5 * time.Second,
		MaxDelay:    5 * time.Second,
		Label:       "vision",
	}
)

func main() {
	cfg := config{
		ollamaURL: getenv("OLLAMA_URL", "http://localhost:11434"),
		// OPENCLAW_TO accepts a single number or a comma-separated list.
		// Every configured target gets the same notifications — useful for
		// multi-user beta testing.
		openclawTargets: openclaw.ParseTargets(mustenv("OPENCLAW_TO")),
	}
	if len(cfg.openclawTargets) == 0 {
		log.Fatal("OPENCLAW_TO must contain at least one number")
	}

	cam := camera.New()
	vis := vision.New(cfg.ollamaURL)
	oc := openclaw.New(cfg.openclawTargets...)
	det := presence.New()

	store := openEventStore()
	go janitor(store)

	log.Printf("GatemanAI listening for presence events — notifying %d target(s): %s",
		len(cfg.openclawTargets), strings.Join(cfg.openclawTargets, ", "))

	for range det.Events() {
		go handleEvent(cam, vis, oc, store)
	}
}

// openEventStore creates ~/.gatemanai/{events.jsonl,photos/} and returns a
// handle. Returns nil if creation fails — the pipeline still works without
// persistence, just without retrospective queries. We log the error and move
// on so a misconfigured home dir doesn't take down the system.
func openEventStore() *events.JSONLStore {
	root, err := events.DefaultRoot()
	if err != nil {
		log.Printf("⚠ event store disabled: %v", err)
		return nil
	}
	s, err := events.NewJSONLStore(root)
	if err != nil {
		log.Printf("⚠ event store disabled: %v", err)
		return nil
	}
	log.Printf("📒 event log: %s (retention 7 days)", root)
	return s
}

// janitor purges events older than 7 days every hour. Single writer of the
// store; mcpserver is read-only so this is safe.
func janitor(store *events.JSONLStore) {
	if store == nil {
		return
	}
	const retention = 7 * 24 * time.Hour
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		removed, err := store.Purge(retention)
		if err != nil {
			log.Printf("⚠ event purge: %v", err)
			continue
		}
		if removed > 0 {
			log.Printf("🧹 purged %d event(s) older than 7 days", removed)
		}
	}
}

// handleEvent pipelines the presence flow:
//  1. Capture (~1s)
//  2. Send the photo immediately to every configured target so users see who's
//     there fast
//  3. Run Gemma description in the same goroutine and send as a follow-up to
//     every target
//
// Each event runs in its own goroutine; Ollama serialises vision calls itself.
// Every external call is wrapped in retry.Do so a single transient blip
// (Wi-Fi flap, OpenClaw reconnect, brief Ollama pause) doesn't lose the event.
func handleEvent(cam *camera.Camera, vis *vision.Client, oc *openclaw.Client, store *events.JSONLStore) {
	log.Println("▶ Presence pipeline starting")
	eventStart := time.Now()
	eventID := events.NewID(eventStart)

	captureCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	var img []byte
	captureStart := time.Now()
	err := retry.Do(captureCtx, captureRetry, func(ctx context.Context) error {
		var err error
		img, err = cam.Capture(ctx)
		return err
	})
	cancel()
	if err != nil {
		log.Printf("✗ capture failed after retries: %v", err)
		return
	}
	log.Printf("📸 captured %s in %s", humanBytes(len(img)), elapsed(captureStart))

	// Persist the photo before sending so the retrospective query path can
	// always find it. If the store is unavailable, log and continue — the
	// live notification flow still works without persistence.
	photoPath := ""
	if store != nil {
		photoPath = store.PhotoPath(eventID)
		if err := os.WriteFile(photoPath, img, 0644); err != nil {
			log.Printf("⚠ persist photo: %v", err)
			photoPath = ""
		}
	}

	sendToAll(oc, "🚪 GatemanAI — live photo:", img)

	log.Println("🧠 Gemma vision describing scene…")
	visionCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var description string
	visionStart := time.Now()
	err = retry.Do(visionCtx, visionRetry, func(ctx context.Context) error {
		var err error
		description, err = vis.Describe(ctx, img)
		return err
	})
	if err != nil {
		log.Printf("✗ vision failed after retries: %v", err)
		appendEvent(store, eventID, eventStart, "[vision failed]", photoPath)
		return
	}
	description = strings.TrimSpace(description)
	if description == "" {
		log.Println("⚠ vision returned empty description — skipping description send")
		appendEvent(store, eventID, eventStart, "[no description]", photoPath)
		return
	}
	log.Printf("🧠 Gemma replied in %s (%d chars): %s", elapsed(visionStart), len(description), description)

	sendToAll(oc, description, nil)

	appendEvent(store, eventID, eventStart, description, photoPath)
	log.Printf("✓ Presence pipeline complete in %s (id=%s)", elapsed(eventStart), eventID)
}

// appendEvent persists a record to the event log. Always called even on
// partial failures so the retrospective query path can show that something
// happened, even if the description is missing.
func appendEvent(store *events.JSONLStore, id string, ts time.Time, description, photoPath string) {
	if store == nil {
		return
	}
	err := store.Append(events.Event{
		ID:          id,
		Timestamp:   ts,
		Description: description,
		PhotoPath:   photoPath,
	})
	if err != nil {
		log.Printf("⚠ persist event: %v", err)
	}
}

// sendToAll fans out a message to every configured target in parallel,
// each with its own retry budget. One target failing does not stop delivery
// to the others, and the total wall-clock time is dominated by the slowest
// target rather than the sum.
func sendToAll(oc *openclaw.Client, message string, img []byte) {
	kind := "text"
	if len(img) > 0 {
		kind = "photo"
	}
	var wg sync.WaitGroup
	for _, to := range oc.Targets() {
		wg.Add(1)
		go func(to string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			sendStart := time.Now()
			err := retry.Do(ctx, sendRetry, func(ctx context.Context) error {
				return oc.Send(ctx, to, message, img)
			})
			if err != nil {
				log.Printf("✗ %s send to %s failed after retries: %v", kind, to, err)
				return
			}
			log.Printf("📤 %s sent to %s in %s", kind, to, elapsed(sendStart))
		}(to)
	}
	wg.Wait()
}

func elapsed(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

type config struct {
	ollamaURL       string
	openclawTargets []string
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}
