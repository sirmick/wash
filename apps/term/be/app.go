// wash-term — a terminal emulator that runs as a normal wash app.
//
// The pty lives in this process (via internal/pty), not in the
// router. The router just forwards raw bytes between the pty fd and
// the browser shell over a wash raw channel. The FE wraps xterm.js.
//
// One pty session per raw channel. Tabs share a window via the
// session map (channel id → *pty.Session).
//
// CLI:
//
//	wash-term                # spawn the user's $SHELL
//	wash-term --exec ARGS... # spawn ARGS instead; tab closes on exit
//
// --exec is what wash-priv uses to run a single command as root in a
// real terminal window — sudo doesn't need to live in this app
// because the privilege escalation already happened at wash-priv's
// fork+exec; wash-term just receives the args and runs them.
package term

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"github.com/sirmick/wash/internal/loginenv"
	"github.com/sirmick/wash/internal/version"
	"io"
	"io/fs"
	"log"
	"os"
	osuser "os/user"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// execArgv is the user-supplied --exec ARGS... When non-nil, openTab
// runs argv directly instead of $SHELL, and the tab autocloses on
// exit. Parsed once in main; immutable thereafter.
var execArgv []string

// loginShell is set by --login; when true, the user's shell is
// invoked with -l (or argv[0]="-bash" equivalent). Cheapest way to
// pick up profile-sourced PATH, prompt colours, and root's
// environment for Root Terminal launches.
var loginShell bool

// loginShellPath picks the shell for --login. It does NOT trust
// $SHELL first — when wash-term has been exec'd by sudo, $SHELL is
// the invoking user's shell, not the current uid's — so
// loginenv.UserShell resolves the effective uid's passwd entry
// before falling back to $SHELL, then /bin/bash.
func loginShellPath() string {
	return loginenv.UserShell()
}

// fixupLoginEnv mutates env (in-place over the os.Setenv side) so
// the spawned shell sees HOME/USER/LOGNAME/SHELL matching the
// EFFECTIVE uid of the wash-term process — not whatever the parent
// (sudo via wash-priv) inherited from the original user's session.
// Without this, root bash --login reads /home/<user>/.bashrc
// because HOME wasn't rewritten by sudo. Called once at startup
// when --login was passed.
func fixupLoginEnv() {
	u, err := osuser.Current()
	if err != nil {
		return
	}
	if u.HomeDir != "" {
		_ = os.Setenv("HOME", u.HomeDir)
	}
	if u.Username != "" {
		_ = os.Setenv("USER", u.Username)
		_ = os.Setenv("LOGNAME", u.Username)
	}
	if sh := loginenv.ShellFromPasswd(u.Uid); sh != "" {
		_ = os.Setenv("SHELL", sh)
	}
}

//go:embed all:assets
var assetsFS embed.FS

type state struct {
	mu       sync.Mutex
	sessions map[uint32]*pty.Session // by channel id
	// statusSent dedupes the tab_status poll: the last status key we
	// pushed per channel, so an idle tab stays off the wire (we only
	// send when root/ssh/user actually flips). Cleared on tab close.
	statusSent map[uint32]string
	// agents holds each tab's merged agent status (T0 poll + OSC 7770
	// events); agentSent is its send-on-change dedupe, same shape as
	// statusSent. See agent.go.
	// autoApprove holds each tab's trailing-output window for the opt-in
	// legacy auto-approve path (autoapprove.go). Absent for every tab
	// until the feature is switched on.
}

// initState allocates the per-window maps. Both entrypoints (Def for the
// standalone shim, run for the multicall dispatch) start here.
func initState() {
	st.sessions = make(map[uint32]*pty.Session)
	st.statusSent = make(map[uint32]string)
}

var st state

// hostName is the short box name ("ai", domain stripped) shown in the
// term status line ("bash as root on ai"). Resolved once — it can't
// change under a running process.
var (
	hostnameOnce sync.Once
	hostnameVal  string
)

func hostName() string {
	hostnameOnce.Do(func() {
		h, _ := os.Hostname()
		hostnameVal = strings.SplitN(h, ".", 2)[0]
	})
	return hostnameVal
}

var def *sdk.AppDef

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-term: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.term",
			Name:            "Terminal",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-term",
			Surface:         sdk.SurfaceWindow,
			Icon:            termIcon,
			Accent:          "#6dc878",
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 800, DefaultHeight: 480},
			// "Root Terminal" launcher row. --login makes root's shell
			// source profile/bashrc so PATH / PS1 / colour match a
			// fresh login rather than wash-priv's parent env.
			RootVariant: &sdk.RootVariant{
				Name: "Root Terminal",
				Args: []string{"--login"},
			},
		},
		Assets:           sub,
		OnReady:          onReady,
		OnCloseRequested: onCloseRequested,
	}
	registry.Register(&registry.App{
		Name:     "wash-term",
		Manifest: def.Manifest,
		Assets:   def.Assets,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
// Parses --exec / --login first so sdk.Main observes a populated
// execArgv / loginShell before it spawns the first openTab.
func Def() *sdk.AppDef {
	parseFlags()
	initState()
	return def
}

// run is the multi-call entrypoint — same flag-parse-then-sdk.Run
// sequence as Def + sdk.Main, but plumbed through registry.App.Run.
func run(ctx context.Context) error {
	parseFlags()
	initState()
	return sdk.Run(ctx, def)
}

// parseFlags processes --exec / --login from os.Args. ContinueOnError
// lets unknown flags pass through unscathed (the SDK consumes nothing
// past the --wash-manifest probe).
func parseFlags() {
	flags := flag.NewFlagSet("wash-term", flag.ContinueOnError)
	useExec := flags.Bool("exec", false, "treat all positional args as the command to run instead of $SHELL")
	login := flags.Bool("login", false, "spawn the user's shell as a login shell (-l)")
	flags.SetOutput(io.Discard)
	_ = flags.Parse(os.Args[1:])
	if *useExec {
		execArgv = flags.Args()
		if len(execArgv) == 0 {
			fmt.Fprintln(os.Stderr, "wash-term: --exec requires at least one positional argument")
			os.Exit(1)
		}
	}
	loginShell = *login
	if loginShell {
		fixupLoginEnv()
	}
}

// onReady fires after handshake. We open the initial pty session in a
// goroutine — OpenChannel must not run on the SDK's read goroutine
// (see internal/sdk/channel.go), but OnReady itself runs on the
// foreground sync path so a goroutine is the safer pattern uniformly.
func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-term ready instance=%s window=%d", instanceID, windowID)
	bus := sdk.NewBus(c)
	registerHandlers(bus)
	go openTab(c, windowID, 80, 24)
	go pollTabStatus(c)
}

// pollTabStatus walks every live PTY's foreground process group once a
// second and pushes a tab_status app_msg to the FE whenever a tab's
// user state changes (normal/root/ssh) — the per-tab badge + status
// line. Send-on-change keeps an idle window silent. One process serves
// one window (InstancingMulti spawns a process per instance), so a
// single ticker on this conn covers all of its tabs; it exits when the
// conn closes.
func pollTabStatus(c *sdk.Conn) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
			st.mu.Lock()
			ids := make([]uint32, 0, len(st.sessions))
			sessions := make([]*pty.Session, 0, len(st.sessions))
			for id, s := range st.sessions {
				ids = append(ids, id)
				sessions = append(sessions, s)
			}
			st.mu.Unlock()
			for i, sess := range sessions {
				id := ids[i]
				fu := sess.ForegroundUser()
				key := fu.State + "\x00" + fu.User + "\x00" + fu.Target
				st.mu.Lock()
				unchanged := st.statusSent[id] == key
				if !unchanged {
					st.statusSent[id] = key
				}
				st.mu.Unlock()
				if unchanged {
					continue
				}
				_ = c.SendAppMsg(map[string]any{
					"kind":       "tab_status",
					"channel_id": uint64(id),
					"state":      fu.State,
					"user":       fu.User,
					"host":       hostName(),
					"target":     fu.Target,
				})
			}
		}
	}
}

// ----- bus types -----

type resizeReq struct {
	ChannelID uint64 `json:"channel_id"`
	Cols      uint64 `json:"cols"`
	Rows      uint64 `json:"rows"`
}

type newTabReq struct {
	Cols uint64 `json:"cols"`
	Rows uint64 `json:"rows"`
}

// execTabReq is the resume path's tab request (§13). Exec is the argv to
// run instead of the user's shell.
type execTabReq struct {
	Cols uint64   `json:"cols"`
	Rows uint64   `json:"rows"`
	Exec []string `json:"exec"`
	Cwd  string   `json:"cwd,omitempty"`
}

type closeTabReq struct {
	ChannelID uint64 `json:"channel_id"`
}

func registerHandlers(b *sdk.Bus) {
	sdk.HandlePersist(b)
	sdk.HandleVoid(b, "resize", func(_ *sdk.Conn, _ string, req resizeReq) error {
		if req.ChannelID == 0 || req.Cols == 0 || req.Rows == 0 {
			return nil
		}
		st.mu.Lock()
		sess := st.sessions[uint32(req.ChannelID)]
		st.mu.Unlock()
		if sess == nil {
			return nil
		}
		return sess.Resize(uint16(req.Cols), uint16(req.Rows))
	})
	// new_tab is the FE's own verb: open a tab running the user's shell.
	// (The privileged "open a tab running THIS command" verb is exec_tab
	// below — a separate kind, because HandleFromVoid drops own-FE
	// messages and because one caller deserves one door.)
	sdk.HandleVoid(b, "new_tab", func(c *sdk.Conn, _ string, req newTabReq) error {
		cols := req.Cols
		rows := req.Rows
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		go openTab(c, c.WindowID(), uint16(cols), uint16(rows))
		return nil
	})
	// exec_tab opens a tab running a specific command — the session-resume
	// path (docs/AGENT_TERM.md §13). Honoured ONLY from com.wash.agentd,
	// and only because a human clicked Resume in the sidebar. The sender is
	// router-attested, so this is a real boundary rather than a convention.
	//
	// (agentd is not a privileged id: an app that could impersonate it is
	// already a process on the user's box and could run the command
	// itself. The check keeps the capability narrow, it does not contain an
	// attacker who is already inside.)
	sdk.HandleFromVoid(b, "exec_tab", func(c *sdk.Conn, _ string, req execTabReq, from wire.Sender) error {
		// agentdAppID is declared beside the verb it guards now that the
		// rest of the agent tier has gone (docs/AGENT_APP.md §10).
		if from.AppID != agentdAppID {
			log.Printf("term: exec_tab refused from=%s", from.AppID)
			return nil
		}
		if len(req.Exec) == 0 {
			return nil
		}
		cols, rows := req.Cols, req.Rows
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		log.Printf("term: exec_tab from=%s argv=%q", from.AppID, req.Exec)
		go openTabExec(c, c.WindowID(), uint16(cols), uint16(rows), req.Exec)
		return nil
	})
	// list_sessions is the FE's mount-time reconcile: a `tab_closed`
	// sent while no shell was attached is dropped by the router (only
	// raw channels are buffered for replay), so the tab list the FE
	// restores from saved app_state can name ptys that died during
	// the detach. The FE asks for the live set after restoring and
	// drops/adopts tabs to match.
	sdk.HandleVoid(b, "list_sessions", func(c *sdk.Conn, _ string, _ struct{}) error {
		st.mu.Lock()
		rows := make([]map[string]any, 0, len(st.sessions))
		for id, s := range st.sessions {
			cols, sessRows := s.Size()
			rows = append(rows, map[string]any{
				"channel_id": uint64(id),
				"shell":      s.Shell,
				// Current pty geometry: the restored xterm opens at
				// this grid so the scrollback replay renders at the
				// width it was emitted for, then reflows to fit.
				"cols": uint64(cols),
				"rows": uint64(sessRows),
			})
		}
		// A window refresh remounts the FE with an empty tab_status map,
		// but the poll only pushes tab_status on change — so a tab whose
		// user state hasn't flipped since would never get its badge back.
		// Forget the dedupe keys here so the next poll tick (≤1s) resends
		// the current status for every live tab and re-seeds the badges.
		st.statusSent = make(map[uint32]string)
		st.mu.Unlock()
		return c.SendAppMsg(map[string]any{"kind": "sessions", "sessions": rows})
	})
	sdk.HandleVoid(b, "close_tab", func(_ *sdk.Conn, _ string, req closeTabReq) error {
		if req.ChannelID == 0 {
			return nil
		}
		st.mu.Lock()
		sess := st.sessions[uint32(req.ChannelID)]
		st.mu.Unlock()
		if sess != nil {
			sess.CloseWithReason("user requested")
		}
		return nil
	})
}

// openTab forks a shell (or --exec argv), opens a raw channel, and
// hands both to internal/pty.Open which wires them with io.Copy
// pairs. Reports tab_opened / tab_closed app_msgs to the FE.
func openTab(c *sdk.Conn, windowID uint32, cols, rows uint16) {
	openTabExec(c, windowID, cols, rows, nil)
}

// openTabExec is openTab with an optional argv override — the resume path
// (§13) runs a specific command instead of the user's shell. An overridden
// tab autocloses when the command exits, matching --exec semantics.
func openTabExec(c *sdk.Conn, windowID uint32, cols, rows uint16, override []string) {
	var argv []string
	switch {
	case len(override) > 0:
		argv = override
	case len(execArgv) > 0:
		// --exec mode: run the requested argv. argv[0] is resolved
		// via $PATH (exec.Command does that). The tab autocloses on
		// exit so wash-priv → wash-term --exec apt-update flows
		// finish naturally.
		argv = execArgv
	case loginShell:
		// --login mode: run the user's shell with -l so it sources
		// profile + rc files. Used by Root Terminal so a sudo'd
		// shell picks up root's PATH / prompt / colour config
		// instead of inheriting wash-priv's parent env.
		argv = []string{loginShellPath(), "-l"}
	}
	// WithWashEnv is what makes a wash terminal a wash terminal: TERM
	// pinned for xterm.js, $WASH_BIN_DIR prepended to PATH so `wash ai`
	// and friends resolve without an absolute path, and the router's
	// WASH_*-namespaced display hints mapped to the real DISPLAY /
	// WAYLAND_DISPLAY a GUI client needs.
	sess, err := pty.Open(context.Background(), c, windowID, cols, rows, argv, pty.WithWashEnv, func(s *pty.Session, reason string) {
		// onClose runs from the pty goroutine when the session ends.
		// Drop from the session map, tell the FE, dismiss the window
		// if no tabs remain.
		st.mu.Lock()
		_, found := st.sessions[s.ID()]
		delete(st.sessions, s.ID())
		delete(st.statusSent, s.ID())
		empty := len(st.sessions) == 0
		st.mu.Unlock()
		if !found {
			return
		}
		_ = c.SendAppMsg(map[string]any{
			"kind":       "tab_closed",
			"channel_id": uint64(s.ID()),
			"reason":     reason,
		})
		if empty {
			// Last tab gone — ask the router to close the window so
			// the user doesn't sit looking at an empty terminal.
			_ = c.ConfirmClose(c.WindowID(), true)
		}
	})
	if err != nil {
		log.Printf("wash-term open: %v", err)
		_ = c.SendAppMsg(map[string]any{"kind": "tab_error", "msg": err.Error()})
		return
	}
	st.mu.Lock()
	st.sessions[sess.ID()] = sess
	st.mu.Unlock()

	log.Printf("wash-term tab opened ch=%d shell=%s pid=%d", sess.ID(), sess.Shell, sess.Cmd().Process.Pid)
	_ = c.SendAppMsg(map[string]any{
		"kind":       "tab_opened",
		"channel_id": uint64(sess.ID()),
		"shell":      sess.Shell,
	})
}

func onCloseRequested(c *sdk.Conn, win uint32) bool {
	// Kill every shell before letting the window go.
	st.mu.Lock()
	sessions := make([]*pty.Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		sessions = append(sessions, s)
	}
	st.mu.Unlock()
	for _, s := range sessions {
		s.CloseWithReason("window closed")
	}
	return true
}

// termIcon — Lucide sprite symbol name. The shell renders this via
// <svg><use href="/icons.svg#terminal"/></svg>; the sprite is built
// from lucide-static at shell build time.
const termIcon = "terminal"

// agentdAppID is the roster service. The only thing wash-term still owes
// it is the exec_tab verb behind a Resume click — the rest of the agent
// tier (hooks, OSC, the decision socket) was retired when agentd started
// hosting sessions itself (docs/AGENT_APP.md §10).
const agentdAppID = "com.wash.agentd"
