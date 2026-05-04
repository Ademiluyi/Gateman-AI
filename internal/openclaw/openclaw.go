package openclaw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
)

// Client triggers OpenClaw agent runs and sends media to WhatsApp.
type Client struct {
	baseURL string
	token   string
	to      string
	http    *http.Client
}

func New(baseURL, token, to string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		to:      to,
		http:    &http.Client{},
	}
}

// Send delivers a WhatsApp message with an optional image via the OpenClaw CLI.
// This uses the push path (openclaw message send --media) which reliably attaches images.
// Pass nil img for text-only messages.
func (c *Client) Send(description string, img []byte) error {
	args := []string{
		"message", "send",
		"--channel", "whatsapp",
		"--target", c.to,
		"--message", description,
	}

	if len(img) > 0 {
		// Write image to a temp file — OpenClaw CLI resolves local paths directly.
		f, err := os.CreateTemp("", "gatemanai-*.jpg")
		if err != nil {
			return fmt.Errorf("temp file: %w", err)
		}
		defer os.Remove(f.Name())

		if _, err := f.Write(img); err != nil {
			f.Close()
			return fmt.Errorf("write image: %w", err)
		}
		f.Close()

		args = append(args, "--media", f.Name())
	}

	out, err := exec.Command("openclaw", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("openclaw send: %w — %s", err, out)
	}
	return nil
}

// Notify runs an OpenClaw agent turn via the webhook and delivers the result to WhatsApp.
// Use this for inbound query responses where Gemma processes the message and calls tools.
func (c *Client) Notify(message string) error {
	payload := map[string]any{
		"message": message,
		"name":    "GatemanAI",
		"deliver": true,
		"channel": "whatsapp",
		"to":      c.to,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.baseURL+"/hooks/agent", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openclaw request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("openclaw returned %d", resp.StatusCode)
	}
	return nil
}
