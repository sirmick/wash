// Adapter discovery and launch (docs/AGENT_APP.md §6, M3).
//
// Neither Claude Code nor Codex speaks ACP natively; both are bridged by
// an adapter that someone else keeps current against the vendor. That is
// the whole maintenance argument for this design, and it is why this file
// is a *table* rather than a pile of per-vendor code.
//
// Discovery is a probe, never a hardcoded assumption: a missing adapter is
// a greyed row in the launcher with a reason, not a failed spawn. Codex
// first because its adapter is a static binary; Claude's needs Node, which
// is why the Claude tier is opt-in in packaging.
package agentd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/version"
)

// initTimeout bounds the handshake. An adapter that has not answered
// `initialize` by now is not going to.
const initTimeout = 30 * time.Second

// Adapter is one way to reach an agent over ACP.
type Adapter struct {
	// ID is what the launcher and the roster call this agent.
	ID string `json:"id"`
	// Name is what a human reads.
	Name string `json:"name"`
	// Command / Args launch the adapter. Probed on PATH.
	Command string   `json:"-"`
	Args    []string `json:"-"`
	// Note explains a greyed row: why this one cannot be used here.
	Note string `json:"note,omitempty"`
	// Available is filled in by Probe.
	Available bool `json:"available"`
}

// adapters is the table. Order is launcher order.
var adapters = []Adapter{
	{
		ID:      "codex",
		Name:    "Codex",
		Command: "codex-acp",
	},
	{
		ID:      "claude",
		Name:    "Claude Code",
		Command: "claude-code-acp",
	},
	{
		ID:      "gemini",
		Name:    "Gemini CLI",
		Command: "gemini",
		// Gemini speaks ACP natively rather than through an adapter.
		Args: []string{"--experimental-acp"},
	},
}

// Probe reports which adapters this box can actually launch. Cheap enough
// to call whenever the launcher opens — it is a PATH lookup per row.
func Probe() []Adapter {
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		p, err := exec.LookPath(a.Command)
		a.Available = err == nil
		if !a.Available {
			a.Note = a.Command + " not on PATH"
		} else {
			log.Printf("agentd: adapter %s -> %s", a.ID, p)
		}
		out = append(out, a)
	}
	return out
}

func adapterByID(id string) (Adapter, bool) {
	for _, a := range adapters {
		if a.ID == id {
			return a, true
		}
	}
	return Adapter{}, false
}

// startHosted launches an adapter, completes the handshake, opens a
// session and puts it on the roster. The returned session is live; the
// caller prompts it.
//
// Every early failure kills the process before returning — a half-started
// adapter is a stray child that outlives the desktop, which is the bug
// class the child-process audit already cost us once.
func startHosted(agentID, cwd string) (*hosted, error) {
	a, ok := adapterByID(agentID)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agentID)
	}
	if cwd == "" {
		cwd = os.Getenv("HOME")
	}
	if !filepath.IsAbs(cwd) {
		// The spec is explicit that cwd MUST be absolute, and a relative
		// one fails inside the agent in a way that reads like a missing
		// directory rather than a protocol error.
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return nil, fmt.Errorf("cwd %q: %w", cwd, err)
		}
		cwd = abs
	}

	cmd := exec.Command(a.Command, a.Args...)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", a.Command, err)
	}

	// The adapter's own diagnostics. Without this, "needs authentication"
	// is indistinguishable from "hung".
	go func() {
		b, _ := io.ReadAll(stderr)
		if len(b) > 0 {
			log.Printf("agentd: adapter %s stderr: %s", a.ID, truncate(b, 2000))
		}
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		})
	}

	hostedMu.Lock()
	hostedSeq++
	key := "acp:" + itoa(hostedSeq)
	hostedMu.Unlock()

	h := &hosted{key: key, agent: a.ID, cwd: cwd, stop: stop}
	h.client = acp.NewClient(stdout, stdin, h)

	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	res, err := h.client.Initialize(ctx, acp.ClientCapabilities{
		// Neither advertised yet: M3 has no terminal or filesystem
		// implementation, so the agent does that work itself and reports
		// the output in session/update. M6 turns these on (§8).
		Fs:       acp.FsCapability{},
		Terminal: false,
	}, acp.Implementation{Name: "wash", Title: "wash", Version: version.Version})
	if err != nil {
		stop()
		return nil, fmt.Errorf("initialize %s: %w", a.ID, err)
	}
	if len(res.AuthMethods) > 0 {
		stop()
		return nil, fmt.Errorf("%s needs authentication (%d methods) — log in with its own CLI first", a.ID, len(res.AuthMethods))
	}

	sid, err := h.client.NewSession(ctx, cwd, nil)
	if err != nil {
		stop()
		return nil, fmt.Errorf("session/new %s: %w", a.ID, err)
	}
	h.sessionID = sid
	h.register()
	log.Printf("agentd: acp session started key=%s agent=%s session=%s cwd=%s", key, a.ID, sid, cwd)

	// The adapter dying is the session ending. Owning the process is what
	// makes this a fact rather than a 60s inference.
	go func() {
		<-h.client.Done()
		h.retire()
	}()
	return h, nil
}

// promptHosted runs one turn. Returns when the agent stops; the roster
// follows along from SessionUpdate underneath.
func promptHosted(h *hosted, text string) {
	h.setState("working", "")
	res, err := h.client.Prompt(context.Background(), h.sessionID, acp.Text(text))
	switch {
	case err != nil:
		log.Printf("agentd: acp prompt key=%s: %v", h.key, err)
		h.setState("done", "error")
	case res.StopReason == acp.StopCancelled:
		h.setState("done", "cancelled")
	default:
		h.setState("done", res.StopReason)
	}
}

func lookupHosted(key string) *hosted {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	return hostedAll[key]
}
