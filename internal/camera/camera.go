package camera

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"
)

type Camera struct {
	piURL string
	http  *http.Client
}

func New() *Camera {
	return &Camera{
		piURL: os.Getenv("PI_URL"),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Capture returns JPEG bytes from the Pi HTTP server or local rpicam-still.
func (c *Camera) Capture(ctx context.Context) ([]byte, error) {
	if c.piURL != "" {
		return c.captureRemote(ctx)
	}
	return c.captureLocal(ctx)
}

func (c *Camera) captureRemote(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.piURL+"/capture", nil)
	if err != nil {
		return nil, fmt.Errorf("pi capture build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pi capture request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pi capture returned %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func (c *Camera) captureLocal(ctx context.Context) ([]byte, error) {
	path := fmt.Sprintf("/tmp/gatemanai-%d.jpg", time.Now().UnixMilli())

	cmd := exec.CommandContext(ctx, "rpicam-still", "-o", path, "-n", "-t", "1", "--width", "640", "--height", "480")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("rpicam-still: %w — %s", err, out)
	}
	defer os.Remove(path)

	return os.ReadFile(path)
}
