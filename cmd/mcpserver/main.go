// mcpserver is a Model Context Protocol stdio server that exposes a capture_door tool.
// OpenClaw spawns this binary and Gemma calls the tool when it needs to see the door.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/openclaw"
)

func main() {
	piURL := os.Getenv("PI_URL")
	to := os.Getenv("OPENCLAW_TO")
	log.Printf("mcpserver start: PI_URL=%q OPENCLAW_TO=%q", piURL, to)
	cam := camera.New()
	oc := openclaw.New(to)

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

		captureCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		img, err := cam.Capture(captureCtx)
		cancel()
		if err != nil {
			return toolError(req.ID, fmt.Sprintf("camera error: %v", err))
		}

		// Send the photo directly via OpenClaw CLI — no Gemma vision call.
		// On 8GB RAM the second Gemma call (vision + agent reply) crashes the gateway,
		// so for inbound we send the raw photo and let the user see for themselves.
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = oc.Send(sendCtx, "Live photo from your door:", img)
		cancel()
		if err != nil {
			return toolError(req.ID, fmt.Sprintf("send error: %v", err))
		}

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
