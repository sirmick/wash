package remote

import (
	"context"
	"testing"
)

// TestSupervisorShutdownCancelsAllHosts — (REVIEW-RECONNECT H5) OnTerminate
// must cancel every live host connection's context so its ssh tunnel (and
// host B's --listen-raw router) is reaped, not orphaned to PID 1. Populate
// the supervisor's proc table directly and assert shutdown() cancels each
// context and clears the table.
func TestSupervisorShutdownCancelsAllHosts(t *testing.T) {
	s := &supervisor{procs: map[string]context.CancelFunc{}}

	ctxs := map[string]context.Context{}
	for _, host := range []string{"alice@a", "bob@b", "carol@c"} {
		ctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.procs[host] = cancel
		s.mu.Unlock()
		ctxs[host] = ctx
	}

	s.shutdown()

	for host, ctx := range ctxs {
		select {
		case <-ctx.Done():
		default:
			t.Errorf("host %s: context not cancelled by shutdown — ssh tunnel would leak", host)
		}
	}
	if n := len(s.procs); n != 0 {
		t.Errorf("procs not cleared after shutdown: %d remain", n)
	}

	// Idempotent: a second shutdown (dual-triggered OnTerminate: signal +
	// conn-close) must not panic.
	s.shutdown()
}

// TestMountShutdownNoPanic — mountManager.shutdown() over an entry with a nil
// FUSE server and an empty sftpConn must not panic (both are handled) and
// must clear the entry table.
func TestMountShutdownNoPanic(t *testing.T) {
	m := &mountManager{entries: map[string]*mountEntry{
		"/mnt/x": {server: nil, conn: &sftpConn{}},
	}}
	m.shutdown()
	if n := len(m.entries); n != 0 {
		t.Errorf("entries not cleared after shutdown: %d remain", n)
	}
	m.shutdown() // idempotent
}
