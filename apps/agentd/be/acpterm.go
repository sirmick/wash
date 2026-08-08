// ACP's terminal capability (terminal/create, output, wait_for_exit, kill,
// release) — docs/AGENT_TERMINAL.md.
//
// Advertising it changes who runs the agent's commands. Without it the
// adapter forks `npm test` inside itself, captures the bytes, and reports a
// finished blob: you cannot watch it, search it, or interrupt it, because
// from wash's side nothing ran. With it the process is OURS, and the agent
// holds a reference to it.
//
// agentd owns the pty rather than wash-term, for one decisive reason: these
// terminals must work when no terminal window is open. ACP's lifetime is
// unambiguous — a terminal outlives its process (output still answers after
// exit) and ends when the agent releases it — so ownership follows the
// session, not a window the user may never have opened. The rendering half
// is free either way: raw channels bind to the SHELL, so any FE can mount
// one by id (proven by e2e/tests/agent-pty-spike.spec.ts).

package agentd

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/pty"
)

// defaultOutputLimit applies when the agent does not name one. Big enough
// for a test run's tail, small enough that a hundred terminals cannot eat
// the service.
const defaultOutputLimit = 1 << 20 // 1 MiB

// maxOutputLimit caps what an agent may ask us to retain.
const maxOutputLimit = 8 << 20 // 8 MiB

// terminal is one ACP terminal: a pty we own, plus the record that must
// outlive it. The session is nil once the child has been reaped, but out
// and exit remain answerable until the agent releases the terminal — §1.2
// of the doc, and the reason this struct exists at all rather than the map
// holding *pty.Session directly.
type terminal struct {
	id   string
	sess *pty.Session
	// chID is the raw channel the pty writes to. It is the terminal's id
	// and also the handle an FE needs to render it, which is why the id
	// is not invented separately.
	chID uint32
	// evSeq is the transcript event announcing this terminal. Kept so the
	// event can be COMPLETED when the process ends: the live channel dies
	// with the pty, so without this a command that finishes quickly — or
	// one the agent releases straight away — leaves an empty frame where
	// its output should be. The agent can still read the output through
	// terminal/output; the human has only the transcript.
	evSeq uint64
	key   string
}

var (
	termMu  sync.Mutex
	termAll = map[string]*terminal{}
	// termEarly marks terminals whose pty died before CreateTerminal
	// finished registering their transcript event — a command can exit in
	// that gap. Registration checks it and completes the event it would
	// otherwise have left "running" forever.
	termEarly = map[string]bool{}
)

// CreateTerminal answers terminal/create.
func (h *hosted) CreateTerminal(ctx context.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	if req.Command == "" {
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal/create: no command")
	}
	limit := defaultOutputLimit
	if req.OutputByteLimit != nil && *req.OutputByteLimit > 0 {
		limit = *req.OutputByteLimit
		if limit > maxOutputLimit {
			limit = maxOutputLimit
		}
	}
	// cwd is confined the same way the fs capability confines paths: the
	// folder the user chose when starting the agent. An agent must not be
	// able to run a command somewhere it cannot read.
	cwd := h.cwd
	if req.Cwd != "" {
		abs, err := h.fsFor().Confine(req.Cwd)
		if err != nil {
			log.Printf("agentd: terminal/create REFUSED key=%s cwd=%q root=%q: %v", h.key, req.Cwd, h.cwd, err)
			return acp.CreateTerminalResponse{}, err
		}
		cwd = abs
	}

	// The child must START in cwd. pty.Open does not take a working
	// directory and agentd must not chdir on its own behalf — several
	// sessions run at once — so the cd happens inside the child.
	argv := terminalCwdWrapper(append([]string{req.Command}, req.Args...), cwd)
	sess, err := pty.Open(ctx, h.conn, 0, 80, 24, argv,
		func(env []string) []string { return withEnv(env, req.Env, cwd) },
		func(s *pty.Session, reason string) {
			code, sig, _ := s.ExitStatus()
			log.Printf("agentd: terminal exited key=%s ch=%d reason=%s code=%d signal=%s",
				h.key, s.ID(), reason, code, sig)
			h.completeTerminalEvent(strconv.FormatUint(uint64(s.ID()), 10))
		},
		pty.WithCapture(limit))
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	id := strconv.FormatUint(uint64(sess.ID()), 10)
	t := &terminal{id: id, sess: sess, chID: sess.ID()}
	termMu.Lock()
	termAll[id] = t
	termMu.Unlock()
	log.Printf("agentd: terminal/create key=%s id=%s argv=%q cwd=%s limit=%d", h.key, id, argv, cwd, limit)
	// Put it in the transcript with its channel, so anything watching this
	// session can mount a live terminal on it. Without this the capability
	// is invisible: the command runs behind wash's boundary, which is
	// better, but nobody can see it happen — and "I can see what it did" is
	// the fallback for not reading every approval.
	if h.conn != nil {
		ev := appendEvent(h.key, Event{
			Kind:    EventTerminal,
			Title:   strings.Join(append([]string{req.Command}, req.Args...), " "),
			Channel: sess.ID(),
			Status:  "running",
		}, time.Now())
		termMu.Lock()
		t.evSeq, t.key = ev.Seq, h.key
		early := termEarly[id]
		delete(termEarly, id)
		termMu.Unlock()
		pushEvent(h.conn, h.key, ev)
		if early {
			// The command already finished while the event was being
			// registered — complete it now or it stays "running" with a
			// dead channel.
			h.completeTerminalEvent(id)
		}
	}
	return acp.CreateTerminalResponse{TerminalID: id}, nil
}

// TerminalOutput answers terminal/output: the captured tail, whether older
// bytes were dropped, and the exit status once there is one. Answering
// after exit is the point — an agent that polls after wait_for_exit is
// reading the result of the command it ran.
func (h *hosted) TerminalOutput(_ context.Context, ref acp.TerminalRef) (acp.TerminalOutputResponse, error) {
	t, err := lookupTerminal(ref)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	text, truncated := t.sess.Output()
	res := acp.TerminalOutputResponse{Output: text, Truncated: truncated}
	if code, sig, exited := t.sess.ExitStatus(); exited {
		res.ExitStatus = &acp.ExitStatus{Signal: sig}
		if sig == "" {
			c := code
			res.ExitStatus.ExitCode = &c
		}
	}
	return res, nil
}

// WaitForExit blocks until the child is reaped. pty.Session.Done closes
// after the status is stored, so the read below cannot race it.
func (h *hosted) WaitForExit(ctx context.Context, ref acp.TerminalRef) (acp.ExitStatus, error) {
	t, err := lookupTerminal(ref)
	if err != nil {
		return acp.ExitStatus{}, err
	}
	select {
	case <-t.sess.Done():
	case <-ctx.Done():
		return acp.ExitStatus{}, ctx.Err()
	}
	code, sig, _ := t.sess.ExitStatus()
	out := acp.ExitStatus{Signal: sig}
	if sig == "" {
		c := code
		out.ExitCode = &c
	}
	return out, nil
}

// KillTerminal ends the child but keeps the record: output and exit status
// stay answerable until release.
func (h *hosted) KillTerminal(_ context.Context, ref acp.TerminalRef) error {
	t, err := lookupTerminal(ref)
	if err != nil {
		return err
	}
	log.Printf("agentd: terminal/kill key=%s id=%s", h.key, t.id)
	t.sess.CloseWithReason("agent killed it")
	return nil
}

// ReleaseTerminal drops the record. The child goes with it if it is still
// running — release is the agent saying it is finished with the terminal,
// not that it wants an orphan.
func (h *hosted) ReleaseTerminal(_ context.Context, ref acp.TerminalRef) error {
	termMu.Lock()
	t := termAll[ref.TerminalID]
	termMu.Unlock()
	if t == nil {
		return nil
	}
	log.Printf("agentd: terminal/release key=%s id=%s", h.key, t.id)
	// Close BEFORE dropping the record. Close fires onClose →
	// completeTerminalEvent (unless the pty's EOF path already did), and
	// that needs the record to find the transcript event. Delete-first
	// left the event stuck "running" on a dead channel whenever the
	// agent's release outran the pty's EOF drain — an agent that runs
	// wait_for_exit → output → release loses that race under load.
	t.sess.CloseWithReason("agent released it")
	termMu.Lock()
	delete(termAll, ref.TerminalID)
	delete(termEarly, ref.TerminalID)
	termMu.Unlock()
	return nil
}

// completeTerminalEvent turns the live terminal in the transcript into its
// result: the captured output and how it ended. Called when the process
// exits, whatever ended it — the agent releasing it, a kill, or the command
// simply finishing.
func (h *hosted) completeTerminalEvent(id string) {
	if h.conn == nil {
		return
	}
	termMu.Lock()
	t := termAll[id]
	if t == nil || t.evSeq == 0 {
		// The pty beat CreateTerminal's registration to the finish line.
		// Leave a marker so registration completes the event instead of
		// dropping this exit on the floor.
		termEarly[id] = true
		termMu.Unlock()
		return
	}
	termMu.Unlock()
	text, truncated := t.sess.Output()
	if truncated {
		text = "…(earlier output dropped)\n" + text
	}
	code, sig, _ := t.sess.ExitStatus()
	status := fmt.Sprintf("exit %d", code)
	if sig != "" {
		status = "killed by " + sig
	}
	ev, ok := updateEvent(t.key, t.evSeq, func(e *Event) {
		e.Text = text
		e.Status = status
		// The channel is gone with the pty; leaving it set would have the
		// FE mount a terminal on a dead id and show nothing.
		e.Channel = 0
	})
	if ok {
		pushEvent(h.conn, t.key, ev)
	}
}

func lookupTerminal(ref acp.TerminalRef) (*terminal, error) {
	termMu.Lock()
	t := termAll[ref.TerminalID]
	termMu.Unlock()
	if t == nil {
		// A released terminal and one that never existed are the same
		// error on purpose: the agent's next move is identical.
		return nil, fmt.Errorf("no terminal %q", ref.TerminalID)
	}
	return t, nil
}

// withEnv applies the agent's env vars over the inherited environment and
// pins PWD to the resolved cwd. Later entries win, so an agent can override
// what it inherited without being able to unset the confinement.
func withEnv(inherited []string, vars []acp.EnvVar, cwd string) []string {
	out := make([]string, 0, len(inherited)+len(vars)+1)
	drop := map[string]bool{"PWD": true}
	for _, v := range vars {
		drop[v.Name] = true
	}
	for _, kv := range inherited {
		if i := strings.IndexByte(kv, '='); i > 0 && drop[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	for _, v := range vars {
		out = append(out, v.Name+"="+v.Value)
	}
	return append(out, "PWD="+cwd)
}

// terminalCwdWrapper turns argv into a command that runs in cwd. pty.Open
// starts the child in agentd's own working directory, and agentd must not
// chdir on behalf of one session — several run at once.
func terminalCwdWrapper(argv []string, cwd string) []string {
	if cwd == "" {
		return argv
	}
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return []string{"sh", "-c", "cd " + "'" + strings.ReplaceAll(cwd, "'", `'\''`) + "' && exec " + strings.Join(quoted, " ")}
}
