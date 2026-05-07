package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/doorbell"
	"github.com/ade/gatemanai/internal/openclaw"
	"github.com/ade/gatemanai/internal/vision"
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
func handleRing(cam *camera.Camera, vis *vision.Client, oc *openclaw.Client) {
	captureCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	img, err := cam.Capture(captureCtx)
	cancel()
	if err != nil {
		log.Printf("capture error: %v", err)
		return
	}

	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := oc.Send(sendCtx, "🚪 Doorbell — live photo:", img); err != nil {
		log.Printf("photo send error: %v", err)
	} else {
		log.Println("Photo sent")
	}
	cancel()

	visionCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	description, err := vis.Describe(visionCtx, img)
	if err != nil {
		log.Printf("vision error: %v", err)
		return
	}
	log.Printf("Description: %s", description)

	descCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := oc.Send(descCtx, description, nil); err != nil {
		log.Printf("description send error: %v", err)
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
