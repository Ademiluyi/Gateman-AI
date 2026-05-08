package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/doorbell"
	"github.com/ade/gatemanai/internal/openclaw"
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
		ollamaURL:  getenv("OLLAMA_URL", "http://localhost:11434"),
		openclawTo: mustenv("OPENCLAW_TO"),
	}

	cam := camera.New()
	vis := vision.New(cfg.ollamaURL)
	oc := openclaw.New(cfg.openclawTo)
	bell := doorbell.New()

	log.Println("GatemanAI listening for doorbell...")

	for range bell.Rings() {
		go handleRing(cam, vis, oc)
	}
}

// handleRing pipelines the doorbell flow:
//  1. Capture (~1s)
//  2. Send the photo immediately so the user sees who's there fast
//  3. Run Gemma description in the same goroutine and send as a follow-up
//
// Each ring runs in its own goroutine; Ollama serialises vision calls itself.
// Every external call is wrapped in retry.Do so a single transient blip
// (Wi-Fi flap, OpenClaw reconnect, brief Ollama pause) doesn't lose the ring.
func handleRing(cam *camera.Camera, vis *vision.Client, oc *openclaw.Client) {
	captureCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	var img []byte
	err := retry.Do(captureCtx, captureRetry, func(ctx context.Context) error {
		var err error
		img, err = cam.Capture(ctx)
		return err
	})
	cancel()
	if err != nil {
		log.Printf("capture failed after retries: %v", err)
		return
	}

	sendCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	err = retry.Do(sendCtx, sendRetry, func(ctx context.Context) error {
		return oc.Send(ctx, "🚪 Doorbell — live photo:", img)
	})
	cancel()
	if err != nil {
		log.Printf("photo send failed after retries: %v", err)
	} else {
		log.Println("Photo sent")
	}

	visionCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var description string
	err = retry.Do(visionCtx, visionRetry, func(ctx context.Context) error {
		var err error
		description, err = vis.Describe(ctx, img)
		return err
	})
	if err != nil {
		log.Printf("vision failed after retries: %v", err)
		return
	}
	description = strings.TrimSpace(description)
	if description == "" {
		log.Println("vision returned empty description — skipping description send")
		return
	}
	log.Printf("Description: %s", description)

	descCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = retry.Do(descCtx, sendRetry, func(ctx context.Context) error {
		return oc.Send(ctx, description, nil)
	})
	if err != nil {
		log.Printf("description send failed after retries: %v", err)
	} else {
		log.Println("Description sent")
	}
}

type config struct {
	ollamaURL  string
	openclawTo string
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
