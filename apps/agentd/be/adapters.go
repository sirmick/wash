// Adapter discovery and launch (docs/AGENT_APP.md §6, M3).
//
// Neither Claude Code nor Codex speaks ACP natively; both are bridged by
// an adapter that someone else keeps current against the vendor. That is
// the whole maintenance argument for this design, and it is why this file
// is a *table* rather than a pile of per-vendor code.
//
// Discovery is a probe, never a hardcoded assumption: a missing adapter is
// a greyed row in the launcher with a reason, not a failed spawn.
//
// Both current adapters are npm packages (verified 2026-08-04 against
// claude-agent-acp 0.64.2 and codex-acp 1.1.9) — the earlier belief that
// Codex's was a static Rust binary was wrong, so **Node is a prerequisite
// for the managed tier as a whole**, not just for Claude. A globally
// installed binary is preferred when present; otherwise the package is run
// through npx, which is how most people will actually have it.
package agentd

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/sdk"
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
	// Command / Args launch the adapter when it is installed as a binary.
	Command string   `json:"-"`
	Args    []string `json:"-"`
	// Package is the npm package to fall back to via npx when Command is
	// not on PATH. Empty means there is no fallback.
	Package string `json:"-"`
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
		Package: "@agentclientprotocol/codex-acp",
	},
	{
		ID:      "claude",
		Name:    "Claude Code",
		Command: "claude-agent-acp",
		// Renamed from @zed-industries/claude-code-acp, which now only
		// prints a deprecation warning.
		Package: "@agentclientprotocol/claude-agent-acp",
	},
	{
		ID:      "gemini",
		Name:    "Gemini CLI",
		Command: "gemini",
		// Gemini speaks ACP natively rather than through an adapter.
		Args: []string{"--experimental-acp"},
	},
}

// launch resolves how to actually start an adapter: its own binary if
// installed, else npx with the package. Returns ok=false when neither is
// possible, with a note a human can act on.
func (a Adapter) launch() (cmd string, args []string, note string, ok bool) {
	if p, err := exec.LookPath(a.Command); err == nil {
		return p, a.Args, "", true
	}
	if a.Package == "" {
		return "", nil, a.Command + " not on PATH", false
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		return "", nil, "needs " + a.Command + " on PATH, or node/npx to run " + a.Package, false
	}
	// --yes so a first run does not sit at npm's install prompt with its
	// stdout — which is the ACP wire — waiting on a human.
	return npx, append([]string{"--yes", a.Package}, a.Args...), "via npx " + a.Package, true
}

// Probe reports which adapters this box can actually launch. Cheap enough
// to call whenever the launcher opens — it is a PATH lookup per row.
func Probe() []Adapter {
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		cmd, _, note, ok := a.launch()
		a.Available, a.Note = ok, note
		if ok {
			log.Printf("agentd: adapter %s -> %s %s", a.ID, cmd, note)
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
func startHosted(agentID, cwd string, svcConn *sdk.Conn) (*hosted, error) {
	a, ok := adapterByID(agentID)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agentID)
	}
	cwd, err := resolveCwd(cwd)
	if err != nil {
		return nil, err
	}

	bin, args, _, ok := a.launch()
	if !ok {
		return nil, fmt.Errorf("agent %q is not installed here: %s", agentID, a.Note)
	}
	cmd := exec.Command(bin, args...)
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
		return nil, fmt.Errorf("start %s: %w", bin, err)
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

	h := &hosted{key: key, agent: a.ID, cwd: cwd, stop: stop, conn: svcConn}
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
	sid, err := h.client.NewSession(ctx, cwd, nil)
	if err != nil {
		stop()
		// authMethods advertises what auth is AVAILABLE, not that it is
		// required: claude-agent-acp lists `claude-login` even when the
		// user is already logged in, so refusing on a non-empty list
		// would refuse every working install. The real signal is
		// session/new failing — and then the list is what makes the
		// error actionable.
		if len(res.AuthMethods) > 0 {
			return nil, fmt.Errorf("%s could not open a session; it offers %s — log in with its own CLI first: %w",
				a.ID, authNames(res.AuthMethods), err)
		}
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

// resolveCwd turns what a human typed into the absolute path the protocol
// demands.
//
// Tilde expansion is not a nicety: the launcher is a text field, "~/wash"
// is what people type, and Go's filepath does not expand it — so without
// this it resolved against the ROUTER's working directory and failed with
// a path nobody recognised (observed on the first real run). A relative
// path is still resolved against the router's cwd, which is at least an
// honest interpretation of what was typed; the failure mode that mattered
// was the one that looked absolute and was not.
func resolveCwd(cwd string) (string, error) {
	home, _ := os.UserHomeDir()
	switch {
	case cwd == "", cwd == "~":
		if home == "" {
			return "", fmt.Errorf("no home directory to start in")
		}
		return home, nil
	case strings.HasPrefix(cwd, "~/"):
		if home == "" {
			return "", fmt.Errorf("cannot expand %q: no home directory", cwd)
		}
		cwd = filepath.Join(home, cwd[2:])
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %w", cwd, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		// Say what we resolved to, not just what failed — the original bug
		// was unreadable precisely because the resolved path was hidden.
		return "", fmt.Errorf("folder %q (resolved to %s): %w", cwd, abs, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("folder %q (resolved to %s) is not a directory", cwd, abs)
	}
	return abs, nil
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

// authNames renders the auth methods an adapter offers, for an error a
// human can act on.
func authNames(ms []acp.AuthMethod) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		if m.Description != "" {
			out = append(out, m.Description)
			continue
		}
		out = append(out, m.ID)
	}
	return strings.Join(out, "; ")
}

func lookupHosted(key string) *hosted {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	return hostedAll[key]
}
