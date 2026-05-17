// rtspsource is a drop-in replacement for picapture that pulls frames from
// an existing RTSP camera (Hikvision, Dahua, ONVIF IP cam, ESP32-CAM, etc.)
// instead of a Pi camera. It exposes the same HTTP contract as picapture
// (/capture, /presence, /trigger, /health) so gatemanai and mcpserver point
// at it via PI_URL with no other changes.
//
// The point: a small business that already owns a CCTV DVR doesn't need to
// buy a Pi to use GatemanAI. Run rtspsource on the same laptop the rest of
// the system runs on, point it at the camera's RTSP URL, and the entire
// pipeline (motion → Gemma → WhatsApp + MCP retrospective) works unchanged.
//
// Frame capture shells out to ffmpeg — universally available, no CGO, no
// new Go dependencies. Motion detection reuses internal/motion.
//
// Required env: RTSP_URL=rtsp://user:pass@host:554/Streaming/Channels/101
// Optional env: LISTEN_ADDR (default :9000), plus the same MOTION_* knobs
// picapture uses.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/ade/gatemanai/internal/motion"
)

const (
	maxQueue    = 10
	presenceTTL = 5 * time.Minute
)

var (
	rtspURL    string
	listenAddr string

	pendingMu sync.Mutex
	pending   []time.Time

	notifyCh = make(chan struct{}, 1)

	// captureMu serialises ffmpeg invocations so the motion loop and an
	// inbound /capture HTTP request never contend on the same RTSP pull.
	captureMu sync.Mutex
)

func main() {
	rtspURL = os.Getenv("RTSP_URL")
	if rtspURL == "" {
		log.Fatal("RTSP_URL is required (e.g. rtsp://user:pass@192.168.0.50:554/Streaming/Channels/101)")
	}
	listenAddr = os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":9000"
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Fatal("ffmpeg not found in PATH — install with `brew install ffmpeg` or `apt install ffmpeg`")
	}

	go runMotionLoop(loadMotionConfig())

	http.HandleFunc("/capture", handleCapture)
	http.HandleFunc("/presence", handlePresence)
	http.HandleFunc("/trigger", handleTrigger)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	log.Printf("rtspsource listening on %s — source: %s", listenAddr, redact(rtspURL))
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}

func handleCapture(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	data, err := grabFrame(r.Context())
	if err != nil {
		log.Printf("✗ rtsp capture error: %v", err)
		http.Error(w, "capture failed", http.StatusInternalServerError)
		return
	}

	log.Printf("📸 served capture: %s in %s (from %s)",
		humanBytes(len(data)), time.Since(start).Round(time.Millisecond), r.RemoteAddr)

	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(data)
}

// grabFrame pulls a single JPEG frame from the RTSP source via ffmpeg.
// -rtsp_transport tcp avoids UDP packet loss on flaky Wi-Fi (the common
// failure mode in target deployments).
func grabFrame(ctx context.Context) ([]byte, error) {
	captureMu.Lock()
	defer captureMu.Unlock()

	path := fmt.Sprintf("/tmp/gatemanai-rtsp-%d.jpg", time.Now().UnixMilli())
	defer os.Remove(path)

	// 10s wall clock is enough for a TCP RTSP handshake + first I-frame on
	// any reasonable LAN. Anything slower is a network problem the upstream
	// retry helper will handle.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx,
		"ffmpeg",
		"-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-frames:v", "1",
		"-q:v", "3",
		"-y",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w — %s", err, out)
	}
	return os.ReadFile(path)
}

// handlePresence — long-polls until a presence event is queued or the
// request times out. Same contract as picapture.
func handlePresence(w http.ResponseWriter, r *http.Request) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if claimEvent() {
			w.WriteHeader(http.StatusOK)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-notifyCh:
		case <-time.After(remaining):
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func handleTrigger(w http.ResponseWriter, r *http.Request) {
	log.Printf("🧪 presence triggered via HTTP from %s", r.RemoteAddr)
	recordPresence()
	fmt.Fprintln(w, "triggered")
}

func recordPresence() {
	pendingMu.Lock()
	pending = append(pending, time.Now())
	if len(pending) > maxQueue {
		pending = pending[len(pending)-maxQueue:]
	}
	pendingMu.Unlock()

	select {
	case notifyCh <- struct{}{}:
	default:
	}
}

func claimEvent() bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for len(pending) > 0 {
		first := pending[0]
		pending = pending[1:]
		if time.Since(first) <= presenceTTL {
			return true
		}
	}
	return false
}

// ---- motion loop (mirrors picapture's runMotionLoop, but frames come from RTSP) ----

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
		threshold: envFloat("MOTION_THRESHOLD", 10),
		edgeCrop:  envFloat("MOTION_EDGE_CROP", 0.05),
	}
}

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

		frame, err := grabFrame(context.Background())
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
			log.Printf("motion: change above threshold (diff=%.1f) but in cooldown (%.0fs left)",
				score, (cfg.cooldown - time.Since(lastFire)).Seconds())
		}

		baseline = frame
	}
}

// ---- helpers ----

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// redact strips the userinfo portion of an RTSP URL for log lines so a
// password never lands in the operator console.
func redact(u string) string {
	at := -1
	scheme := -1
	for i := 0; i+2 < len(u); i++ {
		if u[i] == ':' && u[i+1] == '/' && u[i+2] == '/' {
			scheme = i + 3
			break
		}
	}
	if scheme < 0 {
		return u
	}
	for i := scheme; i < len(u); i++ {
		if u[i] == '@' {
			at = i
			break
		}
		if u[i] == '/' {
			break
		}
	}
	if at < 0 {
		return u
	}
	return u[:scheme] + "***@" + u[at+1:]
}

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
