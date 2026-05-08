package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxAttempts: 3, BaseDelay: time.Millisecond, Label: "t"}, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Config{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond, Label: "t"}, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestExhaustsAttempts(t *testing.T) {
	want := errors.New("always")
	calls := 0
	err := Do(context.Background(), Config{MaxAttempts: 3, BaseDelay: time.Millisecond, Label: "t"}, func(context.Context) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped %v, got %v", want, err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestAbortsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := Do(ctx, Config{MaxAttempts: 100, BaseDelay: 50 * time.Millisecond, Label: "t"}, func(context.Context) error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls < 1 {
		t.Fatalf("expected at least 1 call, got %d", calls)
	}
}

func TestRejectsZeroAttempts(t *testing.T) {
	err := Do(context.Background(), Config{MaxAttempts: 0, BaseDelay: time.Millisecond, Label: "t"}, func(context.Context) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error for MaxAttempts=0")
	}
}
