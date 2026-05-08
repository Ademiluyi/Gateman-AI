package doorbell

import (
	"log"
	"net/http"
	"os"
	"time"
)

// Detector listens for doorbell events from the Pi HTTP server.
type Detector struct {
	piURL string
}

func New() *Detector {
	return &Detector{piURL: os.Getenv("PI_URL")}
}

// Rings returns a channel that receives a value each time the doorbell fires.
func (d *Detector) Rings() <-chan struct{} {
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

// pollPi long-polls GET /doorbell on the Pi.
// 200 = event fired, 204 = timeout (retry immediately), other = backoff.
func (d *Detector) pollPi(ch chan<- struct{}) {
	client := &http.Client{Timeout: 35 * time.Second}
	url := d.piURL + "/doorbell"
	log.Printf("Doorbell polling Pi at %s", url)

	backoff := baseBackoff
	for {
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("doorbell poll error: %v — retrying in %s", err, backoff)
			time.Sleep(backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			log.Println("Doorbell event from Pi")
			ch <- struct{}{}
			backoff = baseBackoff
		case http.StatusNoContent:
			// Pi timeout, no event — loop immediately, treat as healthy
			backoff = baseBackoff
		default:
			log.Printf("doorbell poll unexpected status %d — retrying in %s", resp.StatusCode, backoff)
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
	log.Println("Doorbell stub mode — firing once after 3 seconds")
	time.Sleep(3 * time.Second)
	ch <- struct{}{}
}
