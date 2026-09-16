package inference

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A service that is not installed, disabled in the registry, or killed
// mid-job sends no terminal event: the router drops the message to a
// missing recipient and logs it. Generate must not wait for ever — the
// Session Summary window sat on "Summarizing…" with only Cancel to offer.
func TestGenerateGivesUpWhenTheServiceNeverAnswers(t *testing.T) {
	c := &Client{pending: make(map[string]chan pendingResult)}
	c.emit = func(string, any) error { return nil } // nothing is listening

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Generate(ctx, Request{Input: []Part{{Type: "text", Text: "hi"}}})
	if err == nil {
		t.Fatal("Generate returned no error when nothing answered")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waited %s", waited)
	}
}

// A caller that passes no deadline still gets one.
func TestGenerateAppliesADefaultDeadline(t *testing.T) {
	if defaultDeadline <= 0 || defaultDeadline > 5*time.Minute {
		t.Fatalf("defaultDeadline = %s, want a bounded wait", defaultDeadline)
	}
}
