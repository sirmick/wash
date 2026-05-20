// wash-term — a terminal emulator that runs as a normal wash app.
//
// The pty lives in this process (creack/pty), not in the router. The
// router just forwards raw bytes between the pty fd and the browser
// shell over a wash raw channel. The FE wraps xterm.js.
//
// One pty session per raw channel. Layer 3 (tabs) adds multiple
// channels per window; for now: one tab per window.
package main

import (
	"context"
	"embed"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/sirmick/wash/internal/sdk"
)

// isPtyTerm reports whether err is the kind of error you see when a
// pty's slave side has gone away. On Linux, reading the master after
// the slave closes (process exit / pty.Close) returns EIO; on Close,
// further reads can also be ErrClosed. Both mean "session ended,"
// not "something went wrong."
func isPtyTerm(err error) bool {
	return err == nil || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

//go:embed all:assets
var assetsFS embed.FS

const version = "0.0.0"

type session struct {
	pty *os.File
	cmd *exec.Cmd
	ch  *sdk.RawChannel
}

type state struct {
	mu       sync.Mutex
	sessions map[uint32]*session // by channel id
}

var st state

func main() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("wash-term: assets: %v", err)
	}
	st.sessions = make(map[uint32]*session)
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
		OnAppMsg:         onAppMsg,
		OnCloseRequested: onCloseRequested,
	})
}

// onReady fires after handshake. We open the initial pty session in a
// goroutine — OpenChannel must not run on the SDK's read goroutine
// (see internal/sdk/channel.go), but OnReady itself runs on the
// foreground sync path so a goroutine is the safer pattern uniformly.
func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-term ready instance=%s window=%d", instanceID, windowID)
	go openTab(c, windowID, 80, 24)
}

// openTab forks a shell, opens a raw channel to the browser, and
// wires the two together with io.Copy pairs.
func openTab(c *sdk.Conn, windowID uint32, cols, rows uint16) {
	ch, err := c.OpenChannel(context.Background(), windowID)
	if err != nil {
		log.Printf("wash-term open channel: %v", err)
		_ = c.SendAppMsg(map[string]any{"kind": "tab_error", "msg": err.Error()})
		return
	}
	shell := userShell()
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, startErr := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if startErr != nil {
		log.Printf("wash-term pty.Start %s: %v", shell, startErr)
		_ = ch.Close()
		_ = c.SendAppMsg(map[string]any{"kind": "tab_error", "msg": startErr.Error()})
		return
	}
	sess := &session{pty: f, cmd: cmd, ch: ch}
	st.mu.Lock()
	st.sessions[ch.ID()] = sess
	st.mu.Unlock()

	log.Printf("wash-term tab opened ch=%d shell=%s pid=%d", ch.ID(), shell, cmd.Process.Pid)
	_ = c.SendAppMsg(map[string]any{
		"kind":       "tab_opened",
		"channel_id": uint64(ch.ID()),
		"shell":      shell,
	})

	// pty → channel
	go func() {
		_, copyErr := io.Copy(ch, f)
		if !isPtyTerm(copyErr) {
			log.Printf("wash-term pty→ch ch=%d: %v", ch.ID(), copyErr)
		}
		// pty closed — clean up.
		cleanupSession(c, ch.ID(), "pty eof")
	}()
	// channel → pty
	go func() {
		_, copyErr := io.Copy(f, ch)
		if !isPtyTerm(copyErr) {
			log.Printf("wash-term ch→pty ch=%d: %v", ch.ID(), copyErr)
		}
		// Channel torn down — kill the shell so the other goroutine exits.
		_ = cmd.Process.Kill()
	}()
	go func() {
		_ = cmd.Wait()
		// io.Copy goroutines see EOF on pty next.
	}()
}

func cleanupSession(c *sdk.Conn, chID uint32, reason string) {
	st.mu.Lock()
	sess, ok := st.sessions[chID]
	delete(st.sessions, chID)
	st.mu.Unlock()
	if !ok {
		return
	}
	_ = sess.pty.Close()
	_ = sess.ch.Close()
	_ = c.SendAppMsg(map[string]any{
		"kind":       "tab_closed",
		"channel_id": uint64(chID),
		"reason":     reason,
	})
	// If the last tab closed, request window dismissal so the user
	// doesn't sit looking at an empty terminal.
	st.mu.Lock()
	empty := len(st.sessions) == 0
	st.mu.Unlock()
	if empty {
		// Best-effort: ask the router to close the window. We'd
		// usually wait for window.close_requested, but a session
		// with no shells has nothing to do.
		_ = c.ConfirmClose(c.WindowID(), true)
	}
}

// toUint accepts the various concrete number types CBOR may produce
// when a JSON number traverses JSON → router → CBOR → SDK. JSON's
// Unmarshal lands integers in float64; re-encoding through CBOR
// keeps them as float64. We accept whatever the wire delivered.
func toUint(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case float64:
		return uint64(x)
	}
	return 0
}

func onAppMsg(c *sdk.Conn, _ uint32, data any) {
	m, ok := data.(map[any]any)
	if !ok {
		return
	}
	switch kind, _ := m["kind"].(string); kind {
	case "resize":
		chID := toUint(m["channel_id"])
		cols := toUint(m["cols"])
		rows := toUint(m["rows"])
		if chID == 0 || cols == 0 || rows == 0 {
			return
		}
		st.mu.Lock()
		sess := st.sessions[uint32(chID)]
		st.mu.Unlock()
		if sess == nil {
			return
		}
		if err := pty.Setsize(sess.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
			log.Printf("wash-term resize ch=%d: %v", chID, err)
		}
	case "new_tab":
		cols := toUint(m["cols"])
		rows := toUint(m["rows"])
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		go openTab(c, c.WindowID(), uint16(cols), uint16(rows))
	case "close_tab":
		chID := toUint(m["channel_id"])
		if chID == 0 {
			return
		}
		// Closing the channel will let io.Copy goroutines see EOF
		// and cleanupSession finish the job.
		st.mu.Lock()
		sess := st.sessions[uint32(chID)]
		st.mu.Unlock()
		if sess != nil {
			_ = sess.pty.Close()
			_ = sess.cmd.Process.Kill()
		}
	}
}

func onCloseRequested(c *sdk.Conn, win uint32) bool {
	// Kill every shell before letting the window go.
	st.mu.Lock()
	sessions := make([]*session, 0, len(st.sessions))
	for _, s := range st.sessions {
		sessions = append(sessions, s)
	}
	st.mu.Unlock()
	for _, s := range sessions {
		_ = s.cmd.Process.Kill()
		_ = s.pty.Close()
	}
	return true
}

func userShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

// termIcon — a small ">_" prompt-style mark.
const termIcon = "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16' fill='none' stroke='%23eee' stroke-width='1.5' stroke-linecap='round'><polyline points='3,5 6,8 3,11'/><line x1='8' y1='11' x2='13' y2='11'/></svg>"
