package router

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// TestSupersededNotifiesPriorLiveHead is the regression for REVIEW-RECONNECT
// L2's head-steal half: when a newer shell takes the foreground head, a
// still-live predecessor must be told (shell.superseded) so its FE can raise
// an "opened elsewhere" banner instead of going silently dark as its terminal
// channels migrate away.
func TestSupersededNotifiesPriorLiveHead(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, fe1, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, _, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()

	r.registerShell(s1)
	r.reattachChannelsToShell(s1) // s1 is head; no prior head → no notice

	r.registerShell(s2)
	r.reattachChannelsToShell(s2) // s2 takes head → s1 (live) must be told

	type probe struct {
		T   string `json:"t"`
		Msg string `json:"msg"`
	}
	found := make(chan probe, 1)
	go func() {
		for {
			f, err := fe1.ReadFrame()
			if err != nil {
				return
			}
			if f.Channel != ChannelControl {
				continue
			}
			var p probe
			if json.Unmarshal(f.Payload, &p) == nil && p.T == wire.TShellSuperseded {
				found <- p
				return
			}
		}
	}()

	select {
	case p := <-found:
		if p.Msg == "" {
			t.Error("shell.superseded carried an empty message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prior live head never received shell.superseded")
	}
}

// TestSupersededNotSentToSelf: re-attaching the current head (no change of
// connection) must not emit a superseded notice to itself.
func TestSupersededNotSentToSelf(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	s1, fe1, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()

	r.registerShell(s1)
	r.reattachChannelsToShell(s1)
	r.reattachChannelsToShell(s1) // same shell re-attaches → no self-notice

	// Drain briefly: any frame that decodes as shell.superseded is a bug.
	done := make(chan bool, 1)
	go func() {
		for {
			f, err := fe1.ReadFrame()
			if err != nil {
				return
			}
			if f.Channel != ChannelControl {
				continue
			}
			var p struct {
				T string `json:"t"`
			}
			if json.Unmarshal(f.Payload, &p) == nil && p.T == wire.TShellSuperseded {
				done <- true
				return
			}
		}
	}()
	select {
	case <-done:
		t.Fatal("a shell was told it superseded itself")
	case <-time.After(300 * time.Millisecond):
		// no superseded frame — correct
	}
}

// TestClaimHeadTakesHeadBack: a superseded shell's "Use here"
// (shell.claim_head on its still-open connection) makes it the head again —
// its terminal channel migrates back and the shell that held the head is
// told it was superseded in turn. It used to re-dial, which is a no-op on
// an open socket, so the button did nothing.
func TestClaimHeadTakesHeadBack(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})

	s1, _, c1 := newTestShellSession(t)
	s1.router = r
	defer c1()
	s2, fe2, c2 := newTestShellSession(t)
	s2.router = r
	defer c2()

	r.registerShell(s1)
	r.reattachChannelsToShell(s1)
	term := &channelBinding{
		channelID: 42,
		kind:      wire.ChannelKindGeneric,
		shell:     s1,
		buf:       newRingBuffer(ChannelScrollbackBytes),
	}
	r.registerChannel(term)

	r.registerShell(s2)
	r.reattachChannelsToShell(s2) // s2 steals the head

	superseded := make(chan struct{}, 1)
	go func() {
		for {
			f, err := fe2.ReadFrame()
			if err != nil {
				return
			}
			if f.Channel != ChannelControl {
				continue
			}
			var p struct {
				T string `json:"t"`
			}
			if json.Unmarshal(f.Payload, &p) == nil && p.T == wire.TShellSuperseded {
				superseded <- struct{}{}
				return
			}
		}
	}()

	payload, err := json.Marshal(wire.NewShellClaimHead())
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.dispatch(wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: payload}); err != nil {
		t.Fatalf("dispatch claim_head: %v", err)
	}

	if got := r.headShellOrAny(); got != s1 {
		t.Fatalf("after claim, head = %p, want s1 %p", got, s1)
	}
	term.shellMu.Lock()
	owner := term.shell
	term.shellMu.Unlock()
	if owner != s1 {
		t.Errorf("terminal channel owner = %p, want migrated back to s1 %p", owner, s1)
	}
	select {
	case <-superseded:
	case <-time.After(2 * time.Second):
		t.Fatal("the shell that lost the head never received shell.superseded")
	}
}
