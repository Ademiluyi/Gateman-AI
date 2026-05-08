package camera

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCaptureRemoteSuccess(t *testing.T) {
	want := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0x02, 0x03}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capture" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(want)
	}))
	defer srv.Close()

	cam := &Camera{piURL: srv.URL, http: &http.Client{Timeout: 2 * time.Second}}
	got, err := cam.Capture(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("payload mismatch: got %v, want %v", got, want)
	}
}

func TestCaptureRemoteNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cam := &Camera{piURL: srv.URL, http: &http.Client{Timeout: 2 * time.Second}}
	_, err := cam.Capture(context.Background())
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected error to mention 500, got: %v", err)
	}
}

func TestCaptureRemoteContextCancel(t *testing.T) {
	// Server hangs forever; cancel the context and expect a quick failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	cam := &Camera{piURL: srv.URL, http: &http.Client{Timeout: 5 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := cam.Capture(ctx)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("Capture did not honour context cancel quickly: took %s", time.Since(start))
	}
}

func TestNewReadsPiURLEnv(t *testing.T) {
	t.Setenv("PI_URL", "http://example.invalid:9000")
	cam := New()
	if cam.piURL != "http://example.invalid:9000" {
		t.Fatalf("expected piURL from env, got %q", cam.piURL)
	}
	if cam.http == nil {
		t.Fatal("expected non-nil http client")
	}
}
