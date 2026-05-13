package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/ade/gatemanai/internal/motion"
)

// Motion detection configuration (read from env at startup).
//
//	MOTION_ENABLED        bool    default true        — turn the loop on/off
//	MOTION_INTERVAL_MS    int     default 3000        — sleep between captures
//	MOTION_COOLDOWN_S     int     default 30          — quiet window after a fire
//	MOTION_THRESHOLD      float64 default 20          — mean-abs-diff threshold (0-255)
//	MOTION_EDGE_CROP      float64 default 0.05        — ignore outer N% of frame
//
// All four are independently tunable so the operator can dial sensitivity
// without rebuilding.
type motionConfig struct {
	enabled   bool
	interval  time.Duration
	cooldown  time.Duration
	threshold float64
	edgeCrop  float64
}

func loadMotionConfig() motionConfig {
	return motionConfig{
		enabled:   envBool("MOTION_ENABLED", true),
		interval:  time.Duration(envInt("MOTION_INTERVAL_MS", 3000)) * time.Millisecond,
		cooldown:  time.Duration(envInt("MOTION_COOLDOWN_S", 30)) * time.Second,
		threshold: envFloat("MOTION_THRESHOLD", 20),
		edgeCrop:  envFloat("MOTION_EDGE_CROP", 0.05),
	}
}

// motionMu serialises rpicam-still invocations so the detection loop and an
// inbound /capture HTTP request never invoke the camera simultaneously —
// rpicam-still doesn't tolerate concurrent access to the camera device.
var motionMu sync.Mutex

func runMotionLoop(cfg motionConfig) {
	if !cfg.enabled {
		log.Println("Motion detection disabled (MOTION_ENABLED=false)")
		return
	}

	det := motion.New()
	det.EdgeCropPct = cfg.edgeCrop

	log.Printf("Motion detection enabled — interval=%s cooldown=%s threshold=%.1f edge_crop=%.2f",
		cfg.interval, cfg.cooldown, cfg.threshold, cfg.edgeCrop)

	var (
		baseline []byte
		lastFire time.Time
	)
	for {
		time.Sleep(cfg.interval)

		frame, err := captureForMotion()
		if err != nil {
			log.Printf("motion: capture failed: %v", err)
			continue
		}

		if baseline == nil {
			baseline = frame
			continue
		}

		score, err := det.Diff(baseline, frame)
		if err != nil {
			log.Printf("motion: diff failed: %v", err)
			baseline = frame
			continue
		}

		cooledDown := time.Since(lastFire) >= cfg.cooldown
		if score >= cfg.threshold && cooledDown {
			log.Printf("🚶 motion detected (diff=%.1f ≥ %.1f)", score, cfg.threshold)
			recordPresence()
			lastFire = time.Now()
		} else if score >= cfg.threshold {
			// In cooldown: log at debug level only — keep operator console quiet.
			log.Printf("motion: change above threshold (diff=%.1f) but in cooldown (%.0fs left)",
				score, (cfg.cooldown - time.Since(lastFire)).Seconds())
		}

		// Baseline always drifts to the most recent frame so a settled scene
		// (e.g. someone left a package) becomes the new "normal" instead of
		// triggering forever.
		baseline = frame
	}
}

// captureForMotion takes a fresh JPEG via rpicam-still under the camera mutex.
// Returns the JPEG bytes; the caller decides what to do with them.
func captureForMotion() ([]byte, error) {
	motionMu.Lock()
	defer motionMu.Unlock()

	path := fmt.Sprintf("/tmp/gatemanai-motion-%d.jpg", time.Now().UnixMilli())
	cmd := exec.Command("rpicam-still", "-o", path, "-n", "-t", "1", "--width", "640", "--height", "480")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("rpicam-still: %w — %s", err, out)
	}
	defer os.Remove(path)
	return os.ReadFile(path)
}

// ---- env helpers ----

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off":
		return false
	}
	log.Printf("warning: %s=%q not a bool, using default %v", key, v, fallback)
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("warning: %s=%q not an int, using default %d", key, v, fallback)
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("warning: %s=%q not a float, using default %.2f", key, v, fallback)
		return fallback
	}
	return f
}
