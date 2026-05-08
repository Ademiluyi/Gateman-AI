// Command doctor verifies that every external dependency GatemanAI relies on
// is reachable and configured correctly. It prints a one-line PASS / FAIL for
// each check and exits non-zero if any check fails so it can be wired into a
// pre-flight script before recording the demo or starting the system.
//
// Usage:
//
//	doctor [-pi-url URL] [-ollama-url URL] [-model NAME]
//
// Environment variables override flag defaults: PI_URL, OLLAMA_URL,
// OPENCLAW_TO. The required model name defaults to gemma4:e2b-8k.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	piURL := flag.String("pi-url", env("PI_URL", "http://localhost:9000"), "URL of the Pi picapture HTTP server")
	ollamaURL := flag.String("ollama-url", env("OLLAMA_URL", "http://localhost:11434"), "URL of the Ollama HTTP server")
	model := flag.String("model", "gemma4:e2b-8k", "Required Ollama model name")
	flag.Parse()

	checks := []check{
		{"PI_URL reachable", checkURL(*piURL + "/health")},
		{"Pi /capture endpoint", checkPiCapture(*piURL)},
		{"Ollama reachable", checkURL(*ollamaURL + "/api/tags")},
		{fmt.Sprintf("Ollama has model %q", *model), checkOllamaModel(*ollamaURL, *model)},
		{"OpenClaw gateway running", checkOpenClawGateway},
		{"OpenClaw MCP server registered", checkOpenClawMCP},
		{"OPENCLAW_TO env var set", checkEnv("OPENCLAW_TO")},
	}

	failed := 0
	for _, c := range checks {
		err := c.fn()
		if err == nil {
			fmt.Printf("PASS  %s\n", c.label)
			continue
		}
		fmt.Printf("FAIL  %s — %v\n", c.label, err)
		failed++
	}

	fmt.Println()
	if failed == 0 {
		fmt.Println("All checks passed. System is ready.")
		return
	}
	fmt.Printf("%d check(s) failed.\n", failed)
	os.Exit(1)
}

type check struct {
	label string
	fn    func() error
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func httpClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func checkURL(url string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

func checkPiCapture(piURL string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, piURL+"/capture", nil)
		if err != nil {
			return err
		}
		resp, err := httpClient().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		// JPEG magic bytes start with 0xFF 0xD8 0xFF.
		if len(body) < 3 || body[0] != 0xFF || body[1] != 0xD8 || body[2] != 0xFF {
			return fmt.Errorf("response not a JPEG (%d bytes)", len(body))
		}
		return nil
	}
}

func checkOllamaModel(ollamaURL, model string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaURL+"/api/tags", nil)
		if err != nil {
			return err
		}
		resp, err := httpClient().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var payload struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return fmt.Errorf("decode /api/tags: %w", err)
		}
		for _, m := range payload.Models {
			if m.Name == model {
				return nil
			}
		}
		return fmt.Errorf("not found in Ollama; run: ollama show gemma4:e2b --modelfile > /tmp/Modelfile && echo 'PARAMETER num_ctx 8192' >> /tmp/Modelfile && ollama create %s -f /tmp/Modelfile", model)
	}
}

func checkOpenClawGateway() error {
	out, err := exec.Command("openclaw", "gateway", "status").CombinedOutput()
	if err != nil {
		return fmt.Errorf("openclaw gateway status failed: %w", err)
	}
	// "Service: LaunchAgent (loaded)" or similar appears when the gateway is up.
	if !strings.Contains(strings.ToLower(string(out)), "loaded") &&
		!strings.Contains(strings.ToLower(string(out)), "running") {
		return fmt.Errorf("gateway not loaded/running")
	}
	return nil
}

func checkOpenClawMCP() error {
	out, err := exec.Command("openclaw", "mcp", "list").CombinedOutput()
	if err != nil {
		return fmt.Errorf("openclaw mcp list failed: %w — %s", err, out)
	}
	if !strings.Contains(string(out), "gatemanai-camera") {
		return fmt.Errorf("gatemanai-camera not registered; run: openclaw mcp set gatemanai-camera ...")
	}
	return nil
}

func checkEnv(key string) func() error {
	return func() error {
		if os.Getenv(key) == "" {
			return fmt.Errorf("not set")
		}
		return nil
	}
}
