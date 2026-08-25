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
	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/pkg/sdk"
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
	h, err := dialAdapter(agentID, cwd, svcConn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	res2, err := h.client.NewSession(ctx, h.cwd, nil)
	if err != nil {
		h.stop()
		if len(h.authMethods) > 0 {
			return nil, fmt.Errorf("%s could not open a session; it offers %s — log in with its own CLI first: %w",
				agentID, authNames(h.authMethods), err)
		}
		return nil, fmt.Errorf("session/new %s: %w", agentID, err)
	}
	h.sessionID = res2.SessionID
	// Name the transcript now: before the adapter answers there is no
	// session id to file it under, and every event from here on persists.
	bindTranscript(h.key, h.sessionID, agentID, h.cwd, time.Now())
	h.applyModes(res2.Modes)
	h.register()
	// The settings block arrives with the session, not only on later
	// updates — without this the controls were empty until the agent
	// happened to change something itself.
	h.applyConfigs(res2.ConfigOptions)
	// Record the model now, while the session is young: the index reads
	// the LAST summary, so a session the router outlives still says what
	// it was running rather than only cleanly-retired ones.
	h.noteSession("", time.Now())
	log.Printf("agentd: acp session started key=%s agent=%s session=%s cwd=%s mode=%s modes=%d",
		h.key, agentID, res2.SessionID, h.cwd, res2.Modes.CurrentModeID, len(res2.Modes.AvailableModes))
	return h, nil
}

// dialAdapter launches an adapter and completes the handshake. Shared by
// start and resume, which differ only in session/new vs session/load.
func dialAdapter(agentID, cwd string, svcConn *sdk.Conn) (*hosted, error) {
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
		// fs: the agent reads and writes through wash (acpfs.go), so its
		// file access is confined to the session's cwd, goes through the
		// layer the desktop watches, and is logged in one place. Without
		// this the agent uses its own I/O and the desktop finds out later.
		Fs: acp.FsCapability{ReadTextFile: true, WriteTextFile: true},
		// terminal: the agent hands us its shell commands (acpterm.go)
		// instead of forking them inside the adapter, so the process is
		// wash's — confined to the session cwd, its output captured and
		// answerable after exit, and killable by us. Rendering it as a
		// visible tab is a further step (docs/AGENT_TERMINAL.md M3/M4);
		// the capability is worth having before that, because it moves
		// execution behind wash's boundary rather than the adapter's.
		Terminal: true,
	}, acp.Implementation{Name: "wash", Title: "wash", Version: version.Version})
	if err != nil {
		stop()
		return nil, fmt.Errorf("initialize %s: %w", a.ID, err)
	}
	// The adapter's auth methods are kept for the error message the
	// caller may need: authMethods advertises what is AVAILABLE, not what
	// is required, so it is only meaningful once a session call fails.
	h.authMethods = res.AuthMethods
	return h, nil
}

// promptHosted runs one turn. Returns when the agent stops; the roster
// follows along from SessionUpdate underneath.
func promptHosted(h *hosted, text string) {
	if h.conn != nil {
		pushEvent(h.conn, h.key, appendPrompt(h.key, text, time.Now()))
	}
	h.beginTurn()
	res, err := h.client.Prompt(context.Background(), h.sessionID, acp.Text(text))
	switch {
	case err != nil:
		log.Printf("agentd: acp prompt key=%s: %v", h.key, err)
		// "failed", not "done": a turn that died on an adapter error is
		// not a turn that finished, and reporting it as done made every
		// surface paint it GREEN — indistinguishable from success
		// (docs/AGENT_MESSENGER.md M5).
		h.endTurn("failed", "error")
	case res.StopReason == acp.StopCancelled:
		h.endTurn("done", "cancelled")
	default:
		h.endTurn("done", res.StopReason)
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

// resolveCwd turns what a human typed into the absolute path the protocol
// demands.
//
// Tilde expansion is not a nicety: the launcher is a text field, "~/wash"
// is what people type, and Go's filepath does not expand it — so without
// this it resolved against the ROUTER's working directory and failed with
// a path nobody recognised (observed on the first real run).
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

// resumeHosted reopens a remembered session.
//
// This is what the terminal tier could never do: ACP's session/load makes
// the agent replay the ENTIRE conversation as session/update
// notifications before it answers, so the transcript is repopulated by
// the same handler that fills it live. The history comes back on screen,
// rather than as a terminal scrolled to wherever it happened to be.
func resumeHosted(agentID, cwd, sessionID string, svcConn *sdk.Conn) (*hosted, error) {
	h, err := dialAdapter(agentID, cwd, svcConn)
	if err != nil {
		return nil, err
	}
	if !h.client.Capabilities().LoadSession {
		h.stop()
		return nil, fmt.Errorf("%s cannot reopen sessions (no loadSession capability)", agentID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	h.sessionID = sessionID
	// Register BEFORE loading: the replay arrives as notifications, and
	// they need a roster row and a transcript to land in.
	h.register()
	res, err := h.client.LoadSession(ctx, sessionID, h.cwd, nil)
	if err != nil {
		h.retire()
		return nil, fmt.Errorf("reopen %s: %w", sessionID, err)
	}
	// The same two calls session/new makes. Without them h.modes and
	// h.configs stayed nil, publicModes(nil)/publicConfigs(nil) produced
	// nothing, omitempty dropped Modes and Configs off the roster row,
	// and the FE rendered an empty Session menu — no model, no thinking
	// level — on every resumed session.
	h.applyModes(res.Modes)
	h.applyConfigs(res.ConfigOptions)
	// The replay has landed by the time LoadSession answers, so this is
	// the moment the stored and replayed records can be settled.
	reconcileResume(h.key, sessionID, time.Now())
	// Logged like the started path, so "resumed with settings" and
	// "resumed without" are visible rather than inferred. A started
	// session reported mode=default modes=6 and a resumed one reported
	// nothing at all, which is how the missing calls stayed missing.
	log.Printf("agentd: acp session resumed key=%s agent=%s session=%s cwd=%s mode=%s modes=%d configs=%d",
		h.key, agentID, sessionID, h.cwd, res.Modes.CurrentModeID, len(res.Modes.AvailableModes), len(res.ConfigOptions))
	h.setState("done", "resumed")
	return h, nil
}
