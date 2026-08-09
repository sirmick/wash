package router

import (
	"bytes"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// The behaviour this file pins: a channel nobody is reading keeps FAR more
// history than the steady-state buffer, because the alternative to
// buffering is stalling the process that wrote it — which wash does not do
// (docs/PTY_ROBUST.md: writes never block). Then, once a shell takes
// delivery, the memory goes back.

// writeFrames pushes n payloads of size bytes through the app→shell
// dispatch path, which is where the grow decision lives.
func writeFrames(t *testing.T, inst *AppInstance, channelID uint32, n, size int, fill byte) {
	t.Helper()
	payload := bytes.Repeat([]byte{fill}, size)
	for i := 0; i < n; i++ {
		if err := inst.dispatch(wire.Frame{Channel: channelID, Payload: payload}); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}
}

func TestScrollback_DetachedChannelKeepsMoreThanTheBaseBuffer(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	inst := &AppInstance{AppID: "com.wash.term", router: r}

	const channelID = 77
	b := &channelBinding{
		channelID: channelID,
		app:       inst,
		kind:      wire.ChannelKindGeneric,
		shell:     nil, // detached: the lid is shut
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)

	// A megabyte of output arrives while nobody is watching.
	writeFrames(t, inst, channelID, 64, 16*1024, 'x')

	if got := b.buf.Len(); got <= ChannelScrollbackBytes {
		t.Errorf("detached buffer holds %d bytes, no more than the %d base — growth did not happen",
			got, ChannelScrollbackBytes)
	}
	if got := b.buf.Cap(); got > ChannelScrollbackMaxBytes {
		t.Errorf("cap %d exceeded the %d ceiling", got, ChannelScrollbackMaxBytes)
	}

	// Past the ceiling it goes back to overwriting the oldest bytes —
	// bounded memory, never a stalled writer, never a failed write.
	writeFrames(t, inst, channelID, 512, 16*1024, 'y')
	if got := b.buf.Cap(); got != ChannelScrollbackMaxBytes {
		t.Errorf("cap = %d, want the %d ceiling", got, ChannelScrollbackMaxBytes)
	}
	if got := b.buf.Len(); got != ChannelScrollbackMaxBytes {
		t.Errorf("len = %d, want a full %d buffer", got, ChannelScrollbackMaxBytes)
	}
	// And what it kept is the TAIL — the recent output, which is the part
	// worth having.
	snap := b.buf.Snapshot()
	if snap[len(snap)-1] != 'y' || bytes.Contains(snap, []byte{'x'}) {
		t.Error("buffer kept old bytes over recent ones")
	}
}

// An ATTACHED, keeping-up channel must not grow: the bytes are being
// delivered, so there is nothing to hold.
func TestScrollback_AttachedChannelStaysAtBaseSize(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	inst := &AppInstance{AppID: "com.wash.term", router: r}
	sess := &ShellSession{scheduler: NewScheduler(), drainerDone: make(chan struct{})}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 78
	b := &channelBinding{
		channelID: channelID,
		app:       inst,
		kind:      wire.ChannelKindGeneric,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(1 << 30), // plenty of credit: never behind
	}
	r.registerChannel(b)

	writeFrames(t, inst, channelID, 64, 16*1024, 'x')
	if got := b.buf.Cap(); got != ChannelScrollbackBytes {
		t.Errorf("attached channel grew to %d; it should stay at %d", got, ChannelScrollbackBytes)
	}
	drainAll(t, sess.scheduler)
}

// A channel whose FE has stopped granting credit is in the same position
// as a detached one — the bytes have nowhere to go — so it grows too.
func TestScrollback_BehindChannelGrows(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	inst := &AppInstance{AppID: "com.wash.term", router: r}
	sess := &ShellSession{scheduler: NewScheduler(), drainerDone: make(chan struct{})}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 79
	b := &channelBinding{
		channelID: channelID,
		app:       inst,
		kind:      wire.ChannelKindGeneric,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	b.shellMu.Lock()
	b.behind = true // wedged FE (docs/PTY_ROBUST.md Fix B)
	b.shellMu.Unlock()

	writeFrames(t, inst, channelID, 64, 16*1024, 'z')
	if got := b.buf.Len(); got <= ChannelScrollbackBytes {
		t.Errorf("behind channel holds %d bytes, no more than the %d base", got, ChannelScrollbackBytes)
	}
	drainAll(t, sess.scheduler)
}

// Once a shell has taken the history, the grown buffer is handed back:
// a disconnection costs memory while it lasts, not for the session's life.
func TestScrollback_ShrinksAfterTheShellTakesDelivery(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	inst := &AppInstance{AppID: "com.wash.term", router: r}
	sess := &ShellSession{scheduler: NewScheduler(), drainerDone: make(chan struct{})}
	sess.router = r
	defer sess.scheduler.Close()
	r.registerShell(sess)

	const channelID = 80
	b := &channelBinding{
		channelID: channelID,
		app:       inst,
		kind:      wire.ChannelKindGeneric,
		shell:     sess,
		buf:       newRingBuffer(ChannelScrollbackBytes),
		credit:    NewChannelCredit(0),
	}
	r.registerChannel(b)
	b.shellMu.Lock()
	b.behind = true
	b.shellMu.Unlock()
	writeFrames(t, inst, channelID, 64, 16*1024, 'q')
	grown := b.buf.Cap()
	if grown <= ChannelScrollbackBytes {
		t.Fatalf("buffer did not grow (cap %d)", grown)
	}

	// resync is one of the two delivery points (the other is the reattach
	// replay); both hand the whole history over.
	r.resyncChannel(b)

	if got := b.buf.Cap(); got != ChannelScrollbackBytes {
		t.Errorf("cap after delivery = %d, want back to %d", got, ChannelScrollbackBytes)
	}
	// The recent bytes survive the shrink — the terminal still has its tail.
	if snap := b.buf.Snapshot(); len(snap) == 0 || snap[len(snap)-1] != 'q' {
		t.Error("shrink dropped the recent output")
	}
	drainAll(t, sess.scheduler)
}
