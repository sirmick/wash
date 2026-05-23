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
package main

import (
	"context"
	"embed"
	"flag"
	"io"
	"io/fs"
	"log"
	"os"
	"sync"

	"github.com/sirmick/wash/internal/pty"
	"github.com/sirmick/wash/internal/sdk"
)

// execArgv is the user-supplied --exec ARGS... When non-nil, openTab
// runs argv directly instead of $SHELL, and the tab autocloses on
// exit. Parsed once in main; immutable thereafter.
var execArgv []string

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

type state struct {
	mu       sync.Mutex
	sessions map[uint32]*pty.Session // by channel id
}

var st state

func main() {
	// Parse our flags before sdk.Main; --wash-manifest is intercepted
	// in sdk.Main before any of our code runs, so flag parsing only
	// matters for normal startup. ContinueOnError lets unknown flags
	// pass through unscathed.
	flags := flag.NewFlagSet("wash-term", flag.ContinueOnError)
	useExec := flags.Bool("exec", false, "treat all positional args as the command to run instead of $SHELL")
	flags.SetOutput(io.Discard)
	_ = flags.Parse(os.Args[1:])
	if *useExec {
		execArgv = flags.Args()
		if len(execArgv) == 0 {
			log.Fatal("wash-term: --exec requires at least one positional argument")
		}
	}

	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-term: assets: %v", err)
	}
	st.sessions = make(map[uint32]*pty.Session)
	sdk.Main(&sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.term",
			Name:            "Terminal",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-term",
			Surface:         sdk.SurfaceWindow,
			Icon:            termIcon,
			Instancing:      sdk.InstancingMulti,
			Window:          &sdk.WindowHints{DefaultWidth: 800, DefaultHeight: 480},
		},
		Assets:           sub,
		OnReady:          onReady,
		OnCloseRequested: onCloseRequested,
	})
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
}

// ----- bus types -----

type saveStateReq struct {
	State any `cbor:"state"`
}

type resizeReq struct {
	ChannelID uint64 `cbor:"channel_id"`
	Cols      uint64 `cbor:"cols"`
	Rows      uint64 `cbor:"rows"`
}

type newTabReq struct {
	Cols uint64 `cbor:"cols"`
	Rows uint64 `cbor:"rows"`
}

type closeTabReq struct {
	ChannelID uint64 `cbor:"channel_id"`
}

func registerHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "save_state", func(c *sdk.Conn, _ string, req saveStateReq) error {
		return c.SaveState(req.State)
	})
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
	var argv []string
	if len(execArgv) > 0 {
		// --exec mode: run the requested argv. argv[0] is resolved
		// via $PATH (exec.Command does that). The tab autocloses on
		// exit so wash-priv → wash-term --exec apt-update flows
		// finish naturally.
		argv = execArgv
	}
	sess, err := pty.Open(context.Background(), c, windowID, cols, rows, argv, pty.WithWashEnv, func(s *pty.Session, reason string) {
		// onClose runs from the pty goroutine when the session ends.
		// Drop from the session map, tell the FE, dismiss the window
		// if no tabs remain.
		st.mu.Lock()
		_, found := st.sessions[s.ID()]
		delete(st.sessions, s.ID())
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
