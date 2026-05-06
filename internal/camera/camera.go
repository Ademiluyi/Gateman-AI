package camera

import (
	"fmt"
	"os/exec"
	"time"
)

// Camera captures images from the Pi camera module.
type Camera struct{}

func New() *Camera {
	return &Camera{}
}

// Capture takes a photo and returns the JPEG bytes.
func (c *Camera) Capture() ([]byte, error) {
	path := fmt.Sprintf("/tmp/gatemanai-%d.jpg", time.Now().UnixMilli())

	// rpicam-still is the camera capture tool on Raspbian 13+.
	// -o writes to file, -n disables preview, -t 1 minimises capture delay.
	cmd := exec.Command("rpicam-still", "-o", path, "-n", "-t", "1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("rpicam-still: %w — %s", err, out)
	}

	data, err := exec.Command("cat", path).Output()
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}

	return data, nil
}
