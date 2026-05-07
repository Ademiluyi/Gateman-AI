package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const model = "gemma4:e2b-8k"

const promptImageOnly = `You are helping a deaf person know who is at their door.
Describe the scene in two or three short sentences.

Always include, when visible:
- How many people are present (use a number).
- Each person's clothing, approximate age, and anything they are carrying.
- Any uniform, badge, or company branding (e.g. DHL, police, delivery driver).
- Any vehicle, package, or sign visible behind or near them.

Be factual and specific. Do not invent details. If the scene is empty, say so.`

const promptImageAndAudio = `You are helping a deaf person know who is at their door.
Describe the scene in two or three short sentences, then transcribe anything being said.

Always include, when visible:
- How many people are present (use a number).
- Each person's clothing, approximate age, and anything they are carrying.
- Any uniform, badge, or company branding (e.g. DHL, police, delivery driver).
- Any vehicle, package, or sign visible behind or near them.

Be factual and specific. Do not invent details.`

// Client calls Ollama's local API to describe an image.
type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	// Cold model load on a memory-constrained Mac can take ~30s; first inference adds another 30-60s.
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Gemma 4 vision in Ollama only works via /api/chat — /api/generate silently drops
// the image and the model asks for one in its reply.
type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
}

// Describe sends an image to Gemma 4 and returns a natural language description.
func (c *Client) Describe(ctx context.Context, img []byte) (string, error) {
	return c.DescribeWithAudio(ctx, img, nil)
}

// DumpRequest serialises the request body that Describe would send and writes it to path.
// Debug helper for diffing against a known-good curl payload.
func (c *Client) DumpRequest(img []byte, path string) error {
	body := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "user", Content: promptImageOnly, Images: []string{base64.StdEncoding.EncodeToString(img)}},
		},
		Stream: false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0644)
}

// DescribeWithAudio sends an image and optional audio to Gemma 4.
// Pass nil for audio to describe image only.
// NOTE: Ollama audio support for Gemma 4 E2B is unverified — test after model pull.
func (c *Client) DescribeWithAudio(ctx context.Context, img []byte, audioWAV []byte) (string, error) {
	prompt := promptImageOnly
	images := []string{base64.StdEncoding.EncodeToString(img)}

	if len(audioWAV) > 0 {
		prompt = promptImageAndAudio
		images = append(images, base64.StdEncoding.EncodeToString(audioWAV))
	}

	body := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt, Images: images},
		},
		Stream:  false,
		Options: map[string]any{"num_predict": 120},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("ollama build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama returned %d: %s", resp.StatusCode, string(errBody))
	}

	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Message.Content, nil
}
