// mcpserver is a Model Context Protocol stdio server that exposes a capture_door tool.
// OpenClaw spawns this binary and Gemma calls the tool when it needs to see the door.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ade/gatemanai/internal/camera"
	"github.com/ade/gatemanai/internal/vision"
)

func main() {
	ollamaURL := getenv("OLLAMA_URL", "http://localhost:11434")
	cam := camera.New()
	vis := vision.New(ollamaURL)

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

		resp := dispatch(req, cam, vis)
		if err := enc.Encode(resp); err != nil {
			log.Printf("encode error: %v", err)
		}
	}
}

func dispatch(req rpcRequest, cam *camera.Camera, vis *vision.Client) rpcResponse {
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

		img, err := cam.Capture()
		if err != nil {
			return toolError(req.ID, fmt.Sprintf("camera error: %v", err))
		}

		description, err := vis.Describe(img)
		if err != nil {
			return toolError(req.ID, fmt.Sprintf("vision error: %v", err))
		}

		return ok(req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": description},
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
