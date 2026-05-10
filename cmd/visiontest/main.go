// visiontest exercises the vision + openclaw legs end-to-end on Mac.
// Reads a jpeg, has Gemma 4 describe it, then pushes the description + image to WhatsApp.
//
// Usage: OPENCLAW_TO=+1234567890 go run ./cmd/visiontest <path-to-jpg>
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ade/gatemanai/internal/openclaw"
	"github.com/ade/gatemanai/internal/vision"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: visiontest <path-to-jpg>")
	}
	imgPath := os.Args[1]

	targets := openclaw.ParseTargets(os.Getenv("OPENCLAW_TO"))
	if len(targets) == 0 {
		log.Fatal("OPENCLAW_TO is required (e.g. +19198696632 or +19198696632,+2348012345678)")
	}
	ollamaURL := getenv("OLLAMA_URL", "http://localhost:11434")

	img, err := os.ReadFile(imgPath)
	if err != nil {
		log.Fatalf("read image: %v", err)
	}
	fmt.Printf("Loaded %s (%d bytes)\n", imgPath, len(img))

	vis := vision.New(ollamaURL)
	if dumpPath := os.Getenv("DUMP_REQUEST"); dumpPath != "" {
		if err := vis.DumpRequest(img, dumpPath); err != nil {
			log.Fatalf("dump: %v", err)
		}
		fmt.Printf("Wrote payload to %s — exiting before send.\n", dumpPath)
		return
	}
	fmt.Println("Asking Gemma 4 to describe the image...")
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	description, err := vis.Describe(ctx, img)
	if err != nil {
		log.Fatalf("vision: %v", err)
	}
	fmt.Printf("Gemma replied in %s:\n%s\n\n", time.Since(start).Round(time.Millisecond), description)

	oc := openclaw.New(targets...)
	fmt.Printf("Sending to %d target(s): %v\n", len(targets), targets)
	for _, to := range oc.Targets() {
		if err := oc.Send(ctx, to, description, img); err != nil {
			log.Printf("send to %s: %v", to, err)
		}
	}
	fmt.Println("Done — check your WhatsApp.")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
