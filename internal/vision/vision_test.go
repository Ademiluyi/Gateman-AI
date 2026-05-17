package vision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescribe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/chat" {
			t.Errorf("want /api/chat, got %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		if req.Model != Model {
			t.Errorf("want model %q, got %q", Model, req.Model)
		}
		if len(req.Messages) != 1 {
			t.Fatalf("want 1 message, got %d", len(req.Messages))
		}
		if req.Messages[0].Role != "user" {
			t.Errorf("want role user, got %q", req.Messages[0].Role)
		}
		if !strings.Contains(req.Messages[0].Content, "camera") {
			t.Errorf("prompt missing camera framing: %q", req.Messages[0].Content)
		}
		if len(req.Messages[0].Images) != 1 {
			t.Fatalf("want 1 image, got %d", len(req.Messages[0].Images))
		}
		decoded, err := base64.StdEncoding.DecodeString(req.Messages[0].Images[0])
		if err != nil {
			t.Fatalf("image not base64-encoded: %v", err)
		}
		if string(decoded) != "fake-jpeg-bytes" {
			t.Errorf("image bytes round-trip mismatch")
		}
		if req.Stream {
			t.Errorf("want Stream=false (we want a single JSON response, not SSE)")
		}

		_ = json.NewEncoder(w).Encode(chatResponse{
			Message: chatMessage{Role: "assistant", Content: "A delivery driver in a Jumia vest is at the gate."},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	got, err := c.Describe(context.Background(), []byte("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(got, "Jumia") {
		t.Errorf("response not surfaced to caller: %q", got)
	}
}

func TestDescribe_NonOKReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL).Describe(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error should surface status + body: %v", err)
	}
}

func TestDescribe_DecodeErrorIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Describe(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("want decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("error should mention decode: %v", err)
	}
}

func TestDescribe_HonoursAlreadyCancelledContext(t *testing.T) {
	// If the caller passes an already-cancelled context, Describe should
	// fail fast without making the network call. We never need to hit a
	// real server for this — confirming the http call returns an error
	// with a cancelled ctx is enough.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	_, err := New("http://127.0.0.1:1").Describe(ctx, []byte("x"))
	if err == nil {
		t.Fatal("want error from cancelled context, got nil")
	}
}
