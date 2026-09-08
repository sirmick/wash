package agentd

import (
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Streamed text leaves agentd as deltas, coalesced.
//
// An agent's reply arrives as many small chunks that grow ONE transcript
// event in place. Pushing the accumulated event on every chunk cost
// quadratic bytes per reply — hundreds of frames, each the whole message
// so far — and on a bandwidth-capped link (a VPN) that stream is what
// fills the pipe and queues the desktop's control frames behind it. So a
// continuation of the open message goes out as the text ADDED since the
// last emit (Event.Append), and chunks that land within streamFlushDelay
// of each other go out as one delta. The first chunk of a message, and
// every non-text event, still go out whole, in order.
//
// Each delta carries TextLen, the message's byte length after it, so a
// consumer can tell an append it can apply from one it missed (and ask
// for a replay). A subscriber's snapshot is taken with pending deltas
// flushed first, under the same lock the deltas emit under, so the text
// it holds and the next delta's base agree.

// streamFlushDelay bounds how long a chunk waits to be coalesced with the
// ones behind it. A var so tests can shorten it.
var streamFlushDelay = 50 * time.Millisecond

// transcriptSend is how emitted frames leave. A var so tests can capture
// them without a router.
var transcriptSend = func(conn *sdk.Conn, inst string, msg map[string]any) {
	_ = conn.SendAppMsgToBulk(wire.Recipient{InstanceID: inst}, msg)
}

// keyEmitter is one session's outgoing transcript stream.
type keyEmitter struct {
	// mu orders emission: a delta flush and a snapshot capture+send never
	// interleave, which is what keeps a new subscriber's base consistent.
	mu   sync.Mutex
	conn *sdk.Conn
	key  string
	// The message currently streaming as deltas (0 = none).
	seq  uint64
	kind string
	atMS int64
	// sent is how many bytes of that message's text have gone out.
	sent  int
	dirty bool
	timer *time.Timer
}

var (
	emitMu   sync.Mutex
	emitters = map[string]*keyEmitter{}
)

func emitterFor(key string, conn *sdk.Conn) *keyEmitter {
	emitMu.Lock()
	defer emitMu.Unlock()
	em := emitters[key]
	if em == nil {
		em = &keyEmitter{key: key}
		emitters[key] = em
	}
	if conn != nil {
		em.conn = conn
	}
	return em
}

// dropEmitter flushes and forgets a session's stream (releaseTranscript).
func dropEmitter(key string) {
	emitMu.Lock()
	em := emitters[key]
	delete(emitters, key)
	emitMu.Unlock()
	if em == nil {
		return
	}
	em.mu.Lock()
	em.flushLocked()
	em.mu.Unlock()
}

func isStreamKind(kind string) bool {
	return kind == EventMessage || kind == EventThought
}

// pushEvent sends one event to every watcher of its session — whole, or
// as a coalesced delta when it continues the message already streaming.
//
// Liveness is the watcher's job, not this function's: SendAppMsgTo's error
// path is the local transport, not per-recipient delivery. Router
// instance.gone and transcriptWatchers expire stale recipients.
//
// Bulk class, for the same reason pty output is: even as deltas a reply
// is many frames, and at Interactive they sat in front of the window
// moves and keystrokes the human was making WHILE the agent typed. Bulk
// puts it behind them — losslessly: the scheduler backpressures, it does
// not drop.
//
// The hop that actually shares a pipe with the desktop is wash-ai's
// relay to its FE, and that one marks itself (apps/ai/be/app.go). This
// call marks the stream at its source, so the class is the truth about
// this traffic everywhere it goes rather than a label applied at the end.
func pushEvent(conn *sdk.Conn, key string, e Event) {
	em := emitterFor(key, conn)
	em.mu.Lock()
	defer em.mu.Unlock()
	if isStreamKind(e.Kind) && em.seq != 0 && em.seq == e.Seq {
		// A continuation. Read the text at flush time, not now: the next
		// chunks will have grown it by then, and one delta covers them all.
		em.dirty = true
		if em.timer == nil {
			em.timer = time.AfterFunc(streamFlushDelay, em.flush)
		}
		return
	}
	// Anything else closes the streaming message: what it had pending goes
	// first, so order on the wire is order in the transcript.
	em.flushLocked()
	if isStreamKind(e.Kind) {
		em.seq, em.kind, em.atMS, em.sent = e.Seq, e.Kind, e.AtMS, len(e.Text)
		e.TextLen = len(e.Text)
	} else {
		em.seq = 0
	}
	em.sendLocked(e)
}

func (em *keyEmitter) flush() {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.timer = nil
	em.flushLocked()
}

// flushLocked emits the text added to the streaming message since the
// last emit, if any. Caller holds em.mu.
func (em *keyEmitter) flushLocked() { em.flushLockedExcept("") }

// flushLockedExcept is flushLocked skipping one watcher: the subscriber
// whose snapshot is about to be taken, which will hold this text already
// and has no base to append a delta to.
func (em *keyEmitter) flushLockedExcept(except string) {
	if em.timer != nil {
		em.timer.Stop()
		em.timer = nil
	}
	if !em.dirty {
		return
	}
	em.dirty = false
	full, ok := eventBySeq(em.key, em.seq)
	if !ok {
		return
	}
	if len(full.Text) < em.sent || full.Kind != em.kind {
		// Rewritten rather than grown (never today; defensive): resend whole.
		em.sent = len(full.Text)
		full.TextLen = len(full.Text)
		em.sendLockedExcept(full, except)
		return
	}
	delta := full.Text[em.sent:]
	if delta == "" {
		return
	}
	em.sent = len(full.Text)
	em.sendLockedExcept(Event{
		Seq:     full.Seq,
		Kind:    full.Kind,
		Text:    delta,
		Append:  true,
		TextLen: len(full.Text),
		AtMS:    full.AtMS,
	}, except)
}

func (em *keyEmitter) sendLocked(e Event) { em.sendLockedExcept(e, "") }

func (em *keyEmitter) sendLockedExcept(e Event, except string) {
	for _, inst := range transcriptWatchers(em.key) {
		if inst == except {
			continue
		}
		transcriptSend(em.conn, inst, map[string]any{
			"kind":  "transcript_event",
			"key":   em.key,
			"event": e,
		})
	}
}

// eventBySeq returns a copy of a live transcript's event by seq. Recent
// events are at the tail, and a streaming message is the most recent one,
// so the scan is short.
func eventBySeq(key string, seq uint64) (Event, bool) {
	transMu.Lock()
	defer transMu.Unlock()
	t := trans[key]
	if t == nil {
		return Event{}, false
	}
	for i := len(t.events) - 1; i >= 0; i-- {
		if t.events[i].Seq == seq {
			return t.events[i], true
		}
	}
	return Event{}, false
}

// sendTranscriptSnapshot replays a session's history to one subscriber.
// Under the emitter's lock, with pending deltas flushed first: the text
// the snapshot holds is then exactly the base the next delta appends to.
func sendTranscriptSnapshot(conn *sdk.Conn, instanceID, key string) error {
	em := emitterFor(key, conn)
	em.mu.Lock()
	defer em.mu.Unlock()
	em.flushLockedExcept(instanceID)
	for _, msg := range transcriptSnapshotMsgs(key, snapshot(key)) {
		if err := conn.SendAppMsgToBulk(wire.Recipient{InstanceID: instanceID}, msg); err != nil {
			return err
		}
	}
	return nil
}
