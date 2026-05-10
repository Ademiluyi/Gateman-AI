// mcpserver is a Model Context Protocol stdio server that exposes a capture_door tool.
// OpenClaw spawns this binary and Gemma calls the tool when it needs to see the door.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/openclaw"
)

// mcpLogPath is where mcpserver tees its logs in addition to stderr. OpenClaw
// captures stderr into the gateway's log stream, but for live demo viewing
// it's more useful to `tail -f /tmp/gatemanai-mcpserver.log` in a separate
// terminal alongside gatemanai's stdout.
const mcpLogPath = "/tmp/gatemanai-mcpserver.log"

func main() {
	// Tee logs to a file in addition to stderr so the operator can tail them
	// during a demo without digging through OpenClaw's gateway log.
	if f, err := os.OpenFile(mcpLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}

	piURL := os.Getenv("PI_URL")
	targets := openclaw.ParseTargets(os.Getenv("OPENCLAW_TO"))
	log.Printf("▶ mcpserver start: PI_URL=%q OPENCLAW_TO=%v", piURL, targets)
	cam := camera.New()
	oc := openclaw.New(targets...)

	// MCP uses newline-delimited JSON-RPC over stdio.
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			log.Printf("parse error: %v", err)
			continue
		}

		// Notifications have no id — do not respond.
		if req.ID == nil {
			continue
		}

		resp := dispatch(req, cam, oc)
		if err := enc.Encode(resp); err != nil {
			log.Printf("encode error: %v", err)
		}
	}
}

func dispatch(req rpcRequest, cam *camera.Camera, oc *openclaw.Client) rpcResponse {
	switch req.Method {
	case "initialize":
		return ok(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gatemanai-camera", "version": "1.0.0"},
		})

	case "tools/list":
		return ok(req.ID, map[string]any{
			"tools": []map[string]any{
				{
					"name":        "capture_door",
					"description": "Capture a live photo from the door camera and return a natural language description of who or what is visible.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
			},
		})

	case "tools/call":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name != "capture_door" {
			return rpcError(req.ID, -32602, "unknown tool")
		}

		log.Println("🔧 capture_door tool called by Gemma agent")
		toolStart := time.Now()

		captureCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		captureStart := time.Now()
		img, err := cam.Capture(captureCtx)
		cancel()
		if err != nil {
			log.Printf("✗ camera error: %v", err)
			return toolError(req.ID, fmt.Sprintf("camera error: %v", err))
		}
		log.Printf("📸 captured %s in %s", humanBytes(len(img)), time.Since(captureStart).Round(time.Millisecond))

		// Send the photo directly via OpenClaw CLI — no Gemma vision call.
		// On 8GB RAM the second Gemma call (vision + agent reply) crashes the
		// gateway, so for inbound we send the raw photo and let the user see
		// for themselves. Fan out to every configured target in parallel:
		// total wall-clock time is the slowest send, not the sum.
		var (
			wg       sync.WaitGroup
			anySent  atomic.Bool
			errMu    sync.Mutex
			lastErr  error
		)
		for _, to := range oc.Targets() {
			wg.Add(1)
			go func(to string) {
				defer wg.Done()
				sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				sendStart := time.Now()
				err := oc.Send(sendCtx, to, "Live photo from your door:", img)
				if err != nil {
					errMu.Lock()
					lastErr = err
					errMu.Unlock()
					log.Printf("✗ send to %s failed: %v", to, err)
					return
				}
				log.Printf("📤 photo sent to %s in %s", to, time.Since(sendStart).Round(time.Millisecond))
				anySent.Store(true)
			}(to)
		}
		wg.Wait()

		if !anySent.Load() {
			return toolError(req.ID, fmt.Sprintf("send error: %v", lastErr))
		}

		log.Printf("✓ capture_door complete in %s", time.Since(toolStart).Round(time.Millisecond))

		return ok(req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Photo sent."},
			},
		})

	default:
		return rpcError(req.ID, -32601, "method not found")
	}
}

// ---- JSON-RPC types ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func ok(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func rpcError(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}}
}

func toolError(id json.RawMessage, msg string) rpcResponse {
	return ok(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	})
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
