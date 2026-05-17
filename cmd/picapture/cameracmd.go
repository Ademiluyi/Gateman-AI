package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
)

// Different Raspberry Pi OS releases ship different still-capture binaries.
// We auto-detect at startup and reuse the first one that exists in PATH.
//
//   - rpicam-still     — Bookworm (Debian 12, late 2023+). The current default.
//   - libcamera-still  — Bullseye. Same libcamera stack, older command name.
//   - raspistill       — Buster and earlier. Pre-libcamera userland.
//
// CAMERA_CMD env var overrides auto-detection if set.
var (
	cameraCmdOnce sync.Once
	cameraCmd     string
)

func resolveCameraCmd() string {
	cameraCmdOnce.Do(func() {
		// Honour explicit override first.
		if override := os.Getenv("CAMERA_CMD"); override != "" {
			cameraCmd = override
			log.Printf("camera command: %s (CAMERA_CMD override)", cameraCmd)
			return
		}
		for _, candidate := range []string{"rpicam-still", "libcamera-still", "raspistill"} {
			if _, err := exec.LookPath(candidate); err == nil {
				cameraCmd = candidate
				log.Printf("camera command: %s (auto-detected)", cameraCmd)
				return
			}
		}
		log.Fatalf("no camera command found in PATH (looked for rpicam-still, libcamera-still, raspistill). Install one or set CAMERA_CMD.")
	})
	return cameraCmd
}

// captureArgs returns the argv for a single still capture to `path`. Both
// libcamera-era tools (rpicam-still, libcamera-still) take identical flags.
// raspistill predates them and uses different flag names.
func captureArgs(path string, width, height int) []string {
	switch resolveCameraCmd() {
	case "raspistill":
		return []string{"-o", path, "-n", "-t", "1", "-w", fmt.Sprint(width), "-h", fmt.Sprint(height)}
	default: // rpicam-still, libcamera-still
		return []string{"-o", path, "-n", "-t", "1", "--width", fmt.Sprint(width), "--height", fmt.Sprint(height)}
	}
}
