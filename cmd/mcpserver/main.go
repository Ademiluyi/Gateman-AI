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
	"github.com/ade/gatemanai/internal/events"
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
	store := openEventStore()

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

		resp := dispatch(req, cam, oc, store)
		if err := enc.Encode(resp); err != nil {
			log.Printf("encode error: %v", err)
		}
	}
}

// openEventStore opens the shared event log read-only. gatemanai is the
// writer; mcpserver only reads it (and only sends photos based on what's
// recorded). Returns nil if the store can't be opened — the capture_door
// tool still works, only the recent_events / send_photo tools become
// unavailable in that mode.
func openEventStore() *events.JSONLStore {
	root, err := events.DefaultRoot()
	if err != nil {
		log.Printf("⚠ event store unavailable: %v", err)
		return nil
	}
	s, err := events.NewJSONLStore(root)
	if err != nil {
		log.Printf("⚠ event store unavailable: %v", err)
		return nil
	}
	log.Printf("📒 event log: %s", root)
	return s
}

func dispatch(req rpcRequest, cam *camera.Camera, oc *openclaw.Client, store *events.JSONLStore) rpcResponse {
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
					"description": "Capture a live photo from the door camera right now and send it to the user via WhatsApp. Use when the user wants to know who is at the door this moment.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				},
				{
					"name":        "recent_events",
					"description": "List motion events from the recent past with their times and Gemma-generated scene descriptions. Use when the user asks what happened in the last hour / day, who came by, or anything retrospective. Each event has an id the user can reference to view the photo.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"window": map[string]any{
								"type":        "string",
								"description": "Time window to look back over. Examples: '30m', '1h', '6h', '24h', '7d'. Defaults to '1h' if omitted.",
							},
						},
					},
				},
				{
					"name":        "send_photo",
					"description": "Send the stored photo for a specific past event to the user via WhatsApp. Use after recent_events when the user picks an event to see ('show me the 3pm one'). Requires the event id from a prior recent_events call.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{
								"type":        "string",
								"description": "Event id, as returned by recent_events (e.g. '20260513-150405').",
							},
						},
						"required": []string{"id"},
					},
				},
			},
		})

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcError(req.ID, -32602, "invalid params")
		}

		switch params.Name {
		case "capture_door":
			return handleCaptureDoor(req.ID, cam, oc)
		case "recent_events":
			return handleRecentEvents(req.ID, params.Arguments, store)
		case "send_photo":
			return handleSendPhoto(req.ID, params.Arguments, oc, store)
		default:
			return rpcError(req.ID, -32602, fmt.Sprintf("unknown tool: %s", params.Name))
		}

	default:
		return rpcError(req.ID, -32601, "method not found")
	}
}

// handleCaptureDoor: live capture + fan-out send. Unchanged behaviour from
// the original single-tool dispatch.
func handleCaptureDoor(id json.RawMessage, cam *camera.Camera, oc *openclaw.Client) rpcResponse {
	log.Println("🔧 capture_door tool called by Gemma agent")
	toolStart := time.Now()

	captureCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	captureStart := time.Now()
	img, err := cam.Capture(captureCtx)
	cancel()
	if err != nil {
		log.Printf("✗ camera error: %v", err)
		return toolError(id, fmt.Sprintf("camera error: %v", err))
	}
	log.Printf("📸 captured %s in %s", humanBytes(len(img)), time.Since(captureStart).Round(time.Millisecond))

	if err := fanOutPhoto(oc, img, "Live photo from your door:"); err != nil {
		return toolError(id, err.Error())
	}

	log.Printf("✓ capture_door complete in %s", time.Since(toolStart).Round(time.Millisecond))
	return ok(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": "Photo sent."}},
	})
}

// handleRecentEvents reads the on-disk event log and returns a compact
// formatted list. Description-only — no photos are sent here. The agent
// uses send_photo as a follow-up if the user picks one.
func handleRecentEvents(id json.RawMessage, args json.RawMessage, store *events.JSONLStore) rpcResponse {
	log.Println("🔧 recent_events tool called by Gemma agent")
	if store == nil {
		return toolError(id, "event store unavailable")
	}

	var params struct {
		Window string `json:"window"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &params); err != nil {
			return toolError(id, fmt.Sprintf("invalid arguments: %v", err))
		}
	}
	if params.Window == "" {
		params.Window = "1h"
	}

	window, err := parseWindow(params.Window)
	if err != nil {
		return toolError(id, fmt.Sprintf("invalid window %q: %v", params.Window, err))
	}

	list, err := store.Recent(window)
	if err != nil {
		log.Printf("✗ recent_events: %v", err)
		return toolError(id, fmt.Sprintf("event log read failed: %v", err))
	}

	if len(list) == 0 {
		log.Printf("recent_events: no events in window=%s", window)
		return ok(id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("No events in the last %s.", params.Window)}},
		})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Events in the last %s (%d):\n", params.Window, len(list))
	for _, e := range list {
		fmt.Fprintf(&b, "- %s [id:%s] %s\n",
			e.Timestamp.Local().Format("15:04"), e.ID, e.Description)
	}
	log.Printf("✓ recent_events: returned %d event(s) for window=%s", len(list), window)
	return ok(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": b.String()}},
	})
}

// handleSendPhoto retrieves a stored event by id and sends its JPEG via
// the OpenClaw CLI to every configured target.
func handleSendPhoto(id json.RawMessage, args json.RawMessage, oc *openclaw.Client, store *events.JSONLStore) rpcResponse {
	log.Println("🔧 send_photo tool called by Gemma agent")
	if store == nil {
		return toolError(id, "event store unavailable")
	}

	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.ID == "" {
		return toolError(id, "send_photo requires {id: string}")
	}

	ev, err := store.Get(params.ID)
	if err != nil {
		return toolError(id, fmt.Sprintf("no such event: %s", params.ID))
	}
	if ev.PhotoPath == "" {
		return toolError(id, fmt.Sprintf("event %s has no stored photo", params.ID))
	}
	img, err := os.ReadFile(ev.PhotoPath)
	if err != nil {
		return toolError(id, fmt.Sprintf("read photo %s: %v", ev.PhotoPath, err))
	}

	caption := fmt.Sprintf("From %s — %s", ev.Timestamp.Local().Format("15:04"), ev.Description)
	if err := fanOutPhoto(oc, img, caption); err != nil {
		return toolError(id, err.Error())
	}

	log.Printf("✓ send_photo complete for id=%s", params.ID)
	return ok(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": "Photo sent."}},
	})
}

// fanOutPhoto sends a JPEG to every configured target in parallel. Returns
// nil if at least one target received the message; otherwise returns the
// last error so the caller can surface it.
func fanOutPhoto(oc *openclaw.Client, img []byte, caption string) error {
	var (
		wg      sync.WaitGroup
		anySent atomic.Bool
		errMu   sync.Mutex
		lastErr error
	)
	for _, to := range oc.Targets() {
		wg.Add(1)
		go func(to string) {
			defer wg.Done()
			sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sendStart := time.Now()
			if err := oc.Send(sendCtx, to, caption, img); err != nil {
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
		return fmt.Errorf("send error: %v", lastErr)
	}
	return nil
}

// parseWindow accepts shorthand durations like "30m", "1h", "24h", "7d".
// time.ParseDuration handles s/m/h natively; we add "d" support manually.
func parseWindow(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n := strings.TrimSuffix(s, "d")
		days, err := time.ParseDuration(n + "h")
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", s, err)
		}
		return days * 24, nil
	}
	return time.ParseDuration(s)
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
