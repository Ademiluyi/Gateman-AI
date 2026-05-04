package main

import (
	"log"
	"os"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/doorbell"
	"github.com/ade/gatemanai/internal/openclaw"
	"github.com/ade/gatemanai/internal/vision"
)

func main() {
	cfg := config{
		ollamaURL:     getenv("OLLAMA_URL", "http://localhost:11434"),
		openclawURL:   getenv("OPENCLAW_URL", "http://localhost:18789"),
		openclawToken: mustenv("OPENCLAW_TOKEN"),
		openclawTo:    mustenv("OPENCLAW_TO"),
	}

	cam := camera.New()
	vis := vision.New(cfg.ollamaURL)
	oc := openclaw.New(cfg.openclawURL, cfg.openclawToken, cfg.openclawTo)
	bell := doorbell.New()

	log.Println("GatemanAI listening for doorbell...")

	for range bell.Rings() {
		log.Println("Doorbell triggered — capturing image")

		img, err := cam.Capture()
		if err != nil {
			log.Printf("capture error: %v", err)
			continue
		}

		description, err := vis.Describe(img)
		if err != nil {
			log.Printf("vision error: %v", err)
			continue
		}

		log.Printf("Description: %s", description)

		if err := oc.Send(description, img); err != nil {
			log.Printf("openclaw send error: %v", err)
		}
	}
}

type config struct {
	ollamaURL     string
	openclawURL   string
	openclawToken string
	openclawTo    string
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
