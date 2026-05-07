package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

const gpioPin = "GPIO17"

var doorbellCh = make(chan struct{}, 1)

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

// handleDoorbell long-polls until a doorbell event fires (or 30s timeout).
func handleDoorbell(w http.ResponseWriter, r *http.Request) {
	select {
	case <-doorbellCh:
		w.WriteHeader(http.StatusOK)
	case <-time.After(30 * time.Second):
		w.WriteHeader(http.StatusNoContent) // 204 = no event yet
	case <-r.Context().Done():
	}
}

// handleTrigger manually fires a doorbell event — useful for testing without a physical button.
func handleTrigger(w http.ResponseWriter, r *http.Request) {
	select {
	case doorbellCh <- struct{}{}:
		fmt.Fprintln(w, "triggered")
	default:
		fmt.Fprintln(w, "already pending")
	}
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
			select {
			case doorbellCh <- struct{}{}:
			default: // already pending, drop duplicate
			}
			time.Sleep(2 * time.Second)
		}
	}
}
