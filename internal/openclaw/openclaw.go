package openclaw

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Client sends WhatsApp messages via the OpenClaw CLI.
type Client struct {
	to string
}

func New(to string) *Client {
	return &Client{to: to}
}

// Send delivers a WhatsApp message with an optional image via the OpenClaw CLI.
// Pass nil img for text-only messages.
func (c *Client) Send(ctx context.Context, description string, img []byte) error {
	args := []string{
		"message", "send",
		"--channel", "whatsapp",
		"--target", c.to,
		"--message", description,
	}

	if len(img) > 0 {
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

	out, err := exec.CommandContext(ctx, "openclaw", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("openclaw send: %w — %s", err, out)
	}
	return nil
}
