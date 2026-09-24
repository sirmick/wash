package main

import "testing"

func TestReplyBeforeAwaitIsRetained(t *testing.T) {
	pendingMu.Lock()
	pending["early"] = make(chan any, 1)
	pendingMu.Unlock()
	deliver("early", "file contents")
	if got := await("early"); got != "file contents" {
		t.Fatalf("lost early reply: %v", got)
	}
	pendingMu.Lock()
	_, left := pending["early"]
	pendingMu.Unlock()
	if left {
		t.Fatal("completed request retained")
	}
}
