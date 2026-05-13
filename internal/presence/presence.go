// Package presence long-polls the Pi's /presence endpoint and signals each
// confirmed presence event on a channel. A presence event can originate from
// a GPIO trigger (button or PIR), software motion detection on the camera
// stream, or the manual /trigger endpoint — the abstraction is source-agnostic.
//
// Network errors trigger exponential backoff so the laptop doesn't hammer the
// Pi while it's recovering from a router reboot or Wi-Fi flap; the backoff
// resets to its base on the first successful poll.
package presence

import (
	"log"
	"net/http"
	"os"
	"time"
)

// Detector listens for presence events from the Pi HTTP server.
type Detector struct {
	piURL string
}

func New() *Detector {
	return &Detector{piURL: os.Getenv("PI_URL")}
}

// Events returns a channel that receives a value each time the Pi reports a
// presence event (GPIO trigger, motion detection, or manual /trigger).
func (d *Detector) Events() <-chan struct{} {
	ch := make(chan struct{})

	if d.piURL != "" {
		go d.pollPi(ch)
	} else {
		go d.stub(ch)
	}

	return ch
}

// Backoff bounds for poll errors. Starts at baseBackoff, doubles on each
// consecutive failure up to maxBackoff. Resets on every successful response
// (including a 204 long-poll timeout) so a single bad period doesn't slow
// recovery once the Pi is back.
const (
	baseBackoff = 1 * time.Second
	maxBackoff  = 30 * time.Second
)

// pollPi long-polls GET /presence on the Pi.
// 200 = event fired, 204 = timeout (retry immediately), other = backoff.
func (d *Detector) pollPi(ch chan<- struct{}) {
	client := &http.Client{Timeout: 35 * time.Second}
	url := d.piURL + "/presence"
	log.Printf("Presence polling Pi at %s", url)

	backoff := baseBackoff
	for {
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("presence poll error: %v — retrying in %s", err, backoff)
			time.Sleep(backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			log.Println("Presence event from Pi")
			ch <- struct{}{}
			backoff = baseBackoff
		case http.StatusNoContent:
			// Pi timeout, no event — loop immediately, treat as healthy
			backoff = baseBackoff
		default:
			log.Printf("presence poll unexpected status %d — retrying in %s", resp.StatusCode, backoff)
			time.Sleep(backoff)
			backoff = nextBackoff(backoff)
		}
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// stub fires once after 3 seconds for local testing without a Pi.
func (d *Detector) stub(ch chan<- struct{}) {
	log.Println("Presence stub mode — firing once after 3 seconds")
	time.Sleep(3 * time.Second)
	ch <- struct{}{}
}
