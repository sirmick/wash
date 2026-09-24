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
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
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

// npx installs and launches through one shared cache. Two cold launches can
// otherwise observe each other's half-populated dependency tree: one of the
// failures seen in practice found @openai/codex but not its native optional
// package, while another found codex-acp but not @openai/codex at all. Hold the
// lock only through adapter initialization; live sessions remain concurrent.
var npxLaunchMu sync.Mutex

// builtinEnv points codex-acp at the Codex the user already installed. Without
// this it resolves its bundled @openai/codex dependency from npx's transient
// cache, needlessly depending on a second copy and its platform package.
// User-configured adapters keep complete control of their own environment.
func (a Adapter) builtinEnv(cfg agentpolicy.AgentConfig) []string {
	if a.ID != "codex" || cfg.Command != "" {
		return nil
	}
	if os.Getenv("CODEX_PATH") != "" {
		return nil
	}
	p, err := exec.LookPath("codex")
	if err != nil {
		return nil
	}
	return []string{"CODEX_PATH=" + p}
}

// launch resolves how to actually start an adapter: its own binary if
// installed, else npx with the package. Returns ok=false when neither is
// possible, with a note a human can act on.
func (a Adapter) launch() (cmd string, args []string, note string, ok bool) {
	return a.launchWith(agentpolicy.AgentConfig{})
}

// launchWith is launch with the user's agents.json entry applied. A
// configured `command` replaces the built-in name outright and skips the
// npx fallback: someone who named a binary meant that binary, and quietly
// running a package from the registry instead would be the opposite of
// what they asked for. It is still resolved through PATH, so a bare name
// works as well as an absolute path.
func (a Adapter) launchWith(cfg agentpolicy.AgentConfig) (cmd string, args []string, note string, ok bool) {
	if cfg.Command != "" {
		p, err := exec.LookPath(cfg.Command)
		if err != nil {
			return "", nil, cfg.Command + " (from agents.json) not found", false
		}
		return p, a.Args, "configured: " + cfg.Command, true
	}
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
	pol := hostedPolicy()
	out := make([]Adapter, 0, len(adapters))
	for _, a := range adapters {
		cmd, _, note, ok := a.launchWith(pol.AgentFor(a.ID))
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
	return startHostedCapability(agentID, cwd, svcConn, "", false)
}

// member marks a session launched as a workspace member rather than one that
// may lead a workspace; its workspace bridge lists only the tools it may call.
func startHostedCapability(agentID, cwd string, svcConn *sdk.Conn, capability string, member bool) (*hosted, error) {
	h, err := dialAdapterCapability(agentID, cwd, svcConn, capability, member)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	res2, err := h.client.NewSession(ctx, h.cwd, h.mcp, h.sessionMeta)
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
	if workspaces != nil {
		workspaces.bindSession(h)
	}
	go h.watchExit()
	// The settings block arrives with the session, not only on later
	// updates — without this the controls were empty until the agent
	// happened to change something itself.
	h.applyConfigs(res2.ConfigOptions)
	// Record the model now, while the session is young: the index reads
	// the LAST summary, so a session the router outlives still says what
	// it was running rather than only cleanly-retired ones.
	h.noteSession("", time.Now())
	h.sessionReady.Store(true)
	log.Printf("agentd: acp session started key=%s agent=%s session=%s cwd=%s mode=%s modes=%d mcp=%d",
		h.key, agentID, res2.SessionID, h.cwd, res2.Modes.CurrentModeID, len(res2.Modes.AvailableModes), len(h.mcp))
	return h, nil
}

// dialAdapter launches an adapter and completes the handshake. Shared by
// start and resume, which differ only in session/new vs session/load.
func dialAdapter(agentID, cwd string, svcConn *sdk.Conn) (*hosted, error) {
	return dialAdapterCapability(agentID, cwd, svcConn, "", false)
}
func dialAdapterCapability(agentID, cwd string, svcConn *sdk.Conn, capability string, member bool) (*hosted, error) {
	if capability != "" && (capability != "reviewer" || agentID != "claude") {
		return nil, fmt.Errorf("capability %q unsupported by %s; no session started", capability, agentID)
	}
	a, ok := adapterByID(agentID)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agentID)
	}
	cwd, err := resolveCwd(cwd)
	if err != nil {
		return nil, err
	}

	// agents.json (internal/agentpolicy): the command override, extra
	// args, and the environment to add. Read at LAUNCH, not at boot, so
	// editing the file takes effect on the next session rather than the
	// next router restart.
	pol := hostedPolicy()
	cfg := pol.AgentFor(agentID)
	bin, args, note, ok := a.launchWith(cfg)
	if !ok {
		return nil, fmt.Errorf("agent %q is not installed here: %s", agentID, note)
	}
	if strings.HasPrefix(note, "via npx ") {
		npxLaunchMu.Lock()
		defer npxLaunchMu.Unlock()
	}
	run := pol.Merge(agentID, agentpolicy.Launch{Command: bin, Args: args})
	// Built-ins come first so an explicit agents.json environment entry is
	// appended later and retains the documented user-wins precedence.
	run.Env = append(a.builtinEnv(cfg), run.Env...)
	cmd := exec.Command(run.Command, run.Args...)
	cmd.Dir = cwd
	// Added to the inherited environment, not substituted for it: an
	// adapter that gained an API key must not have lost PATH.
	if len(run.Env) > 0 {
		cmd.Env = append(os.Environ(), run.Env...)
		log.Printf("agentd: adapter %s env+=%d args=%d", agentID, len(run.Env), len(run.Args))
	}
	// Its own process group, so stop() can kill the whole tree. The common
	// launch is `npx --yes <package>`, which is a node wrapper around the
	// node adapter around the agent: killing the pid alone reaped the
	// wrapper and orphaned the rest, still holding its half of the wire.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

	hostedMu.Lock()
	hostedSeq++
	key := "acp:" + itoa(hostedSeq)
	hostedMu.Unlock()

	h := &hosted{capability: capability, key: key, agent: a.ID, cwd: cwd, conn: svcConn, mcp: acpMCPServers(run.MCPServers), stderrDone: make(chan struct{})}

	// Only the injected coordination server is available to restricted reviewers.
	if capability == "reviewer" {
		h.mcp = nil
	}

	// The adapter's own diagnostics. Without this, "needs authentication"
	// is indistinguishable from "hung". The tail is also kept on the
	// session, because when the adapter dies the last thing it said is
	// the one line that explains why — and it belongs in the transcript,
	// not only in a log the person watching the window never sees.
	go func() {
		defer close(h.stderrDone)
		b, _ := io.ReadAll(io.TeeReader(stderr, h.stderrTail()))
		if len(b) > 0 {
			log.Printf("agentd: adapter %s stderr: %s", a.ID, truncate(b, 2000))
		}
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = stdin.Close()
			if cmd.Process != nil {
				killGroup(cmd.Process)
			}
			_ = cmd.Wait()
		})
	}

	h.stop = stop
	if workspaces != nil {
		if err := workspaces.inject(h, member); err != nil {
			h.stop()
			return nil, err
		}
		originalStop := h.stop
		h.stop = func() { workspaces.revoke(h); originalStop() }
	}
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
		h.stop()
		return nil, fmt.Errorf("initialize %s: %w", a.ID, err)
	}
	// The adapter's auth methods are kept for the error message the
	// caller may need: authMethods advertises what is AVAILABLE, not what
	// is required, so it is only meaningful once a session call fails.
	h.authMethods = res.AuthMethods
	if capability != "" {
		h.sessionMeta, err = reviewerMetadata(agentID, res.AgentInfo)
		if err != nil {
			h.stop()
			return nil, err
		}
	}
	return h, nil
}

// promptHosted runs one turn. Returns when the agent stops — with the
// next queued prompt to run, or "" — and the roster follows along from
// SessionUpdate underneath. Callers go through hosted.submitPrompt, which
// owns the turn claim; calling this directly is only right when the turn
// is already claimed (tests).
func promptHosted(h *hosted, t turn) (next turn) {
	text := t.text
	if t.origin != "" {
		body := t.displayText
		if body == "" {
			body = text
		}
		e := appendEvent(h.key, Event{Kind: "collaboration", Text: t.origin + "\n\n" + body}, time.Now())
		if h.conn != nil {
			pushEvent(h.conn, h.key, e)
		}
	} else if h.conn != nil {
		pushEvent(h.conn, h.key, appendPrompt(h.key, text, time.Now()))
		queuePreviewPatch(h.key)
	} else {
		appendPrompt(h.key, text, time.Now())
	}
	h.beginTurn()
	// Text first, then the attachments: the sentence is what frames them,
	// and an adapter reading the blocks in order should see the question
	// before the screenshot it is about.
	blocks := make([]acp.ContentBlock, 0, 1+len(t.blocks))
	if text != "" {
		blocks = append(blocks, acp.Text(text))
	}
	blocks = append(blocks, t.blocks...)
	res, err := h.client.Prompt(context.Background(), h.sessionID, blocks...)
	if workspaces != nil {
		workspaces.captureUsage(h)
		var end error
		if err == nil && res.StopReason == acp.StopCancelled {
			end = workspaces.store.TurnStopped(h.sessionID, t.mailIDs)
		} else {
			end = workspaces.store.TurnEnded(h.sessionID, t.mailIDs, err != nil)
		}
		if e := end; e != nil {
			log.Printf("agentd: workspace turn outcome: %v", e)
		}
		defer workspaces.signal()
	}
	switch {
	case err != nil:
		log.Printf("agentd: acp prompt key=%s: %v", h.key, err)
		// "failed", not "done": a turn that died on an adapter error is
		// not a turn that finished, and reporting it as done made every
		// surface paint it GREEN — indistinguishable from success
		// (docs/AGENT_MESSENGER.md M5). A turn that died because the
		// adapter went away is "exited", which the exit watcher explains.
		if h.closing.Load() {
			h.endTurn("failed", "exited")
		} else {
			h.endTurn("failed", "error")
			// The error itself goes in the transcript. A red dot alone
			// said nothing about WHY — expired auth, a rate limit, a
			// refused request all looked the same — and the person had
			// to find the router log to learn which. The composer stays
			// usable: the session is still up, so the next prompt is the
			// retry.
			h.note("The turn failed: " + turnError(err) + "\n\nThe session is still open — send again to retry.")
		}
		return turn{}
	case res.StopReason == acp.StopCancelled:
		return h.endTurn("done", "cancelled")
	default:
		return h.endTurn("done", res.StopReason)
	}
}

// killGroup ends a process started with Setpgid and everything it forked.
// SIGKILL, not SIGTERM: an adapter is a stateless bridge (the agent's
// own session state is the vendor's and already on disk), and this runs
// on the bus handler's goroutine, so there is nothing to wait politely
// for. The direct kill is the fallback for a process that somehow is not
// its own group leader.
func killGroup(p *os.Process) {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		_ = p.Kill()
	}
}

// turnError is the adapter's error as a person should read it. An RPC
// error's message is the adapter's own words ("authentication required",
// "rate limit exceeded") and is kept verbatim; the client's framing
// prefix is dropped, and a closed wire is named for what it means.
func turnError(err error) string {
	if errors.Is(err, acp.ErrClosed) {
		return "the agent's adapter has gone away"
	}
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "acp: ")
	if i := strings.Index(msg, "rpc "); i == 0 {
		// "rpc -32000: <message>" → "<message>"
		if j := strings.Index(msg, ": "); j > 0 {
			msg = msg[j+2:]
		}
	}
	return msg
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
	capability, member := savedWorkspaceLaunch(sessionID)
	return resumeHostedCapability(agentID, cwd, sessionID, svcConn, capability, member)
}
func resumeHostedCapability(agentID, cwd, sessionID string, svcConn *sdk.Conn, capability string, member bool) (*hosted, error) {
	h, err := dialAdapterCapability(agentID, cwd, svcConn, capability, member)
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
	if workspaces != nil {
		workspaces.bindSession(h)
	}
	go h.watchExit()
	res, err := h.client.LoadSession(ctx, sessionID, h.cwd, h.mcp, h.sessionMeta)
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
	reconcileResume(h.key, sessionID, agentID, h.cwd, time.Now())
	h.journal("agent.resume", "session resumed")
	// Logged like the started path, so "resumed with settings" and
	// "resumed without" are visible rather than inferred. A started
	// session reported mode=default modes=6 and a resumed one reported
	// nothing at all, which is how the missing calls stayed missing.
	log.Printf("agentd: acp session resumed key=%s agent=%s session=%s cwd=%s mode=%s modes=%d configs=%d",
		h.key, agentID, sessionID, h.cwd, res.Modes.CurrentModeID, len(res.Modes.AvailableModes), len(res.ConfigOptions))
	h.setState("done", "resumed")
	h.sessionReady.Store(true)
	return h, nil
}

// acpMCPServers converts wash's config shape (env as a map, because a
// person writes it) to ACP's (env as a list of name/value pairs). Returns
// nil for an empty list, which the client turns into the `[]` the spec
// requires — the value wash sent unconditionally before there was
// anything to put in it.
func acpMCPServers(list []agentpolicy.MCPServer) []acp.McpServer {
	if len(list) == 0 {
		return nil
	}
	out := make([]acp.McpServer, 0, len(list))
	for _, s := range list {
		m := acp.McpServer{Name: s.Name, Command: s.Command, Args: s.Args}
		for _, kv := range agentpolicy.EnvPairs(s.Env) {
			m.Env = append(m.Env, acp.EnvVar{Name: kv[0], Value: kv[1]})
		}
		out = append(out, m)
	}
	return out
}
