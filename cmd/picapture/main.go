package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

const gpioPin = "GPIO17"

// Presence event queue: buffers up to maxQueue events with TTL eviction so
// events during a brief laptop / Wi-Fi outage are not lost, but an event that
// happened hours ago doesn't trigger a stale notification.
const (
	maxQueue    = 10
	presenceTTL = 5 * time.Minute
)

var (
	pendingMu sync.Mutex
	pending   []time.Time

	// notifyCh signals waiting long-poll handlers that a new event is queued.
	// Buffered=1 so the producer (GPIO / motion / HTTP trigger) never blocks
	// on a slow consumer.
	notifyCh = make(chan struct{}, 1)
)

func main() {
	go listenGPIO()
	go runMotionLoop(loadMotionConfig())

	http.HandleFunc("/capture", handleCapture)
	http.HandleFunc("/presence", handlePresence)
	http.HandleFunc("/trigger", handleTrigger)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	addr := ":9000"
	log.Printf("picapture listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleCapture(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	path := fmt.Sprintf("/tmp/gatemanai-%d.jpg", time.Now().UnixMilli())

	// Serialise with the motion detection loop — rpicam-still can't share
	// the camera device with itself.
	motionMu.Lock()
	cmd := exec.Command("rpicam-still", "-o", path, "-n", "-t", "1", "--width", "640", "--height", "480")
	out, err := cmd.CombinedOutput()
	motionMu.Unlock()
	if err != nil {
		log.Printf("✗ rpicam-still error: %v — %s", err, out)
		http.Error(w, "capture failed", http.StatusInternalServerError)
		return
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("✗ read capture file: %v", err)
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}

	log.Printf("📸 served capture: %s in %s (from %s)",
		humanBytes(len(data)), time.Since(start).Round(time.Millisecond), r.RemoteAddr)

	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(data)
}

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

// handlePresence long-polls until a non-stale presence event is queued
// or the request deadline expires.
//
//	200 No Content → no event within the long-poll window
//	200 OK         → an event is consumed
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
			// new event signalled; loop back and try to claim
		case <-time.After(remaining):
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// handleTrigger manually fires a presence event. Useful for testing,
// demo recordings, and any out-of-band signal that bypasses the GPIO
// listener or software motion loop.
func handleTrigger(w http.ResponseWriter, r *http.Request) {
	log.Printf("🧪 presence triggered via HTTP from %s", r.RemoteAddr)
	recordPresence()
	fmt.Fprintln(w, "triggered")
}

// recordPresence appends a timestamp to the pending queue (capped at maxQueue,
// dropping the oldest on overflow) and signals any waiting long-poll handler.
// All presence sources — GPIO, software motion, manual /trigger — funnel here.
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

// claimEvent removes and returns true for the oldest non-stale event,
// dropping any stale events in front of it. Returns false if the queue is
// empty or contains only stale events.
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

// listenGPIO watches GPIO17 for a presence signal. The pin's pull resistor
// and edge direction are selected by the GPIO_TRIGGER_EDGE env var:
//
//   - "falling" (default): active-low — wired to a momentary button to ground,
//     internal pull-up, falling edge on press.
//   - "rising": active-high — wired to a PIR sensor's OUT pin, internal
//     pull-down, rising edge on motion detection.
//
// When sourcing a PIR module later, set GPIO_TRIGGER_EDGE=rising and wire
// VCC/GND/OUT to 5V / GND / GPIO17. No rebuild needed.
func listenGPIO() {
	if _, err := host.Init(); err != nil {
		log.Printf("GPIO init failed: %v — GPIO presence source disabled", err)
		return
	}

	pin := gpioreg.ByName(gpioPin)
	if pin == nil {
		log.Printf("GPIO pin %s not found — GPIO presence source disabled", gpioPin)
		return
	}

	pull, edge, label := gpio.PullUp, gpio.FallingEdge, "button (active-low)"
	if os.Getenv("GPIO_TRIGGER_EDGE") == "rising" {
		pull, edge, label = gpio.PullDown, gpio.RisingEdge, "PIR (active-high)"
	}

	if err := pin.In(pull, edge); err != nil {
		log.Printf("GPIO pin config failed: %v — GPIO presence source disabled", err)
		return
	}

	log.Printf("GPIO presence source listening on %s — mode: %s", gpioPin, label)

	for {
		pin.WaitForEdge(-1)
		// Active-low: pin reads gpio.Low on event. Active-high: pin reads gpio.High.
		want := gpio.Low
		if edge == gpio.RisingEdge {
			want = gpio.High
		}
		if pin.Read() == want {
			log.Printf("🔔 presence detected (GPIO17, %s)", label)
			recordPresence()
			time.Sleep(2 * time.Second) // debounce
		}
	}
}
