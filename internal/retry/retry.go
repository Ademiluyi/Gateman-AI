// Package retry runs an operation with exponential backoff between attempts.
// Used to absorb transient failures common on home Wi-Fi: router reboots,
// WhatsApp Web reconnects, brief Ollama swap pauses.
package retry

import (
	"context"
	"errors"
	"log"
	"time"
)

// Config controls a retry loop. Zero values are not valid; use Default()
// or set fields explicitly.
type Config struct {
	// MaxAttempts is the total number of attempts (not retries). Must be >= 1.
	MaxAttempts int
	// BaseDelay is the wait before the second attempt. Subsequent waits double
	// up to MaxDelay.
	BaseDelay time.Duration
	// MaxDelay caps exponential growth. Set equal to BaseDelay to disable backoff.
	MaxDelay time.Duration
	// Label is used in log messages so the operator can tell which call is retrying.
	Label string
}

// Do runs fn until it succeeds, the context is cancelled, or attempts are exhausted.
// Returns nil on first success, the last error otherwise. Honours ctx between attempts.
func Do(ctx context.Context, cfg Config, fn func(context.Context) error) error {
	if cfg.MaxAttempts < 1 {
		return errors.New("retry: MaxAttempts must be >= 1")
	}

	delay := cfg.BaseDelay
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt == cfg.MaxAttempts {
			break
		}

		log.Printf("%s attempt %d/%d failed: %v — retrying in %s",
			cfg.Label, attempt, cfg.MaxAttempts, err, delay)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2
		if cfg.MaxDelay > 0 && delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}

	return lastErr
}
