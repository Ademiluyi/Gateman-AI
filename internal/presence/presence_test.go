package presence

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPollPi200ProducesEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := &Detector{piURL: srv.URL}
	ch := make(chan struct{}, 1)
	go d.pollPi(ch)

	select {
	case <-ch:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("expected event within 2s, got none")
	}
}

func TestPollPi204DoesNotProduceEvent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := &Detector{piURL: srv.URL}
	ch := make(chan struct{}, 1)
	go d.pollPi(ch)

	// Give the loop time to poll a few times and confirm no event arrives.
	select {
	case <-ch:
		t.Fatal("did not expect event on 204")
	case <-time.After(200 * time.Millisecond):
	}

	if calls.Load() < 1 {
		t.Fatalf("expected at least 1 poll, got %d", calls.Load())
	}
}

func TestNextBackoffCapsAtMax(t *testing.T) {
	cases := []struct {
		in, want time.Duration
	}{
		{1 * time.Second, 2 * time.Second},
		{8 * time.Second, 16 * time.Second},
		{16 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second},
		{60 * time.Second, 30 * time.Second},
	}
	for _, c := range cases {
		got := nextBackoff(c.in)
		if got != c.want {
			t.Errorf("nextBackoff(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}
