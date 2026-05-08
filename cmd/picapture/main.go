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

// Doorbell event queue: buffers up to maxQueue presses with TTL eviction so
// rings during a brief laptop / Wi-Fi outage are not lost, but a press that
// happened hours ago doesn't trigger a stale notification.
const (
	maxQueue    = 10
	doorbellTTL = 5 * time.Minute
)

var (
	pendingMu sync.Mutex
	pending   []time.Time

	// notifyCh signals waiting long-poll handlers that a new press is queued.
	// Buffered=1 so the GPIO callback never blocks on a slow consumer.
	notifyCh = make(chan struct{}, 1)
)

func main() {
	go listenGPIO()

	http.HandleFunc("/capture", handleCapture)
	http.HandleFunc("/doorbell", handleDoorbell)
	http.HandleFunc("/trigger", handleTrigger)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	addr := ":9000"
	log.Printf("picapture listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleCapture(w http.ResponseWriter, r *http.Request) {
	path := fmt.Sprintf("/tmp/gatemanai-%d.jpg", time.Now().UnixMilli())

	cmd := exec.Command("rpicam-still", "-o", path, "-n", "-t", "1", "--width", "640", "--height", "480")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("rpicam-still error: %v — %s", err, out)
		http.Error(w, "capture failed", http.StatusInternalServerError)
		return
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(data)
}

// handleDoorbell long-polls until a non-stale doorbell event is queued
// or the request deadline expires.
//
//	200 No Content → no event within the long-poll window
//	200 OK         → an event is consumed
func handleDoorbell(w http.ResponseWriter, r *http.Request) {
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
			// new press signalled; loop back and try to claim
		case <-time.After(remaining):
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// handleTrigger manually fires a doorbell event — useful for testing without a physical button.
func handleTrigger(w http.ResponseWriter, r *http.Request) {
	recordPress()
	fmt.Fprintln(w, "triggered")
}

// recordPress appends a timestamp to the pending queue (capped at maxQueue,
// dropping the oldest on overflow) and signals any waiting long-poll handler.
func recordPress() {
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

// claimEvent removes and returns true for the oldest non-stale press,
// dropping any stale events in front of it. Returns false if the queue is
// empty or contains only stale events.
func claimEvent() bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for len(pending) > 0 {
		first := pending[0]
		pending = pending[1:]
		if time.Since(first) <= doorbellTTL {
			return true
		}
	}
	return false
}

func listenGPIO() {
	if _, err := host.Init(); err != nil {
		log.Printf("GPIO init failed: %v — doorbell disabled", err)
		return
	}

	pin := gpioreg.ByName(gpioPin)
	if pin == nil {
		log.Printf("GPIO pin %s not found — doorbell disabled", gpioPin)
		return
	}

	if err := pin.In(gpio.PullUp, gpio.FallingEdge); err != nil {
		log.Printf("GPIO pin config failed: %v — doorbell disabled", err)
		return
	}

	log.Printf("Doorbell listening on %s", gpioPin)

	for {
		pin.WaitForEdge(-1)
		if pin.Read() == gpio.Low {
			log.Println("Doorbell pressed")
			recordPress()
			time.Sleep(2 * time.Second) // debounce
		}
	}
}
