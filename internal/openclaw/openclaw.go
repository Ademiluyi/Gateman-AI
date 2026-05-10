// Package openclaw delivers WhatsApp messages by shelling out to the OpenClaw
// CLI. The local OpenClaw gateway holds the WhatsApp Web session, so the
// command itself just hands off the target number, message text, and optional
// JPEG attachment.
package openclaw

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Client sends WhatsApp messages via the OpenClaw CLI to one or more targets.
type Client struct {
	targets []string
}

// New constructs a Client from one or more E.164-formatted phone numbers.
// Empty strings and surrounding whitespace are stripped.
func New(targets ...string) *Client {
	c := &Client{}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t != "" {
			c.targets = append(c.targets, t)
		}
	}
	return c
}

// ParseTargets splits a comma-separated list (e.g. "+1555...,+234...") into
// a normalised slice of E.164 numbers. Whitespace and empty entries are dropped.
func ParseTargets(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		t := strings.TrimSpace(raw)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Targets returns the configured recipient list. Safe to read concurrently.
func (c *Client) Targets() []string { return c.targets }

// Send delivers a WhatsApp message to a single target via the OpenClaw CLI.
// Pass nil img for text-only messages. Callers loop over Targets() so a single
// target failing doesn't block the others.
func (c *Client) Send(ctx context.Context, to, description string, img []byte) error {
	if to == "" {
		return fmt.Errorf("openclaw send: empty target")
	}

	args := []string{
		"message", "send",
		"--channel", "whatsapp",
		"--target", to,
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
