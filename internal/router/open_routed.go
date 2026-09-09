package router

import (
	"bytes"
	"fmt"
	"os"

	"github.com/sirmick/wash/pkg/wire"
)

// open.routed — the router's "a file just got opened" notice to the session
// app (docs/IMAGES.md open-routing, P2 "recent files").
//
// The router is the one party that sees every way a file reaches its handler:
// an open.request (fm double-click), a spawn.request carrying Open (edit's
// "Reveal in Files"), and a terminal-launched `wash-edit --open <path>` that
// fresh-attaches with the path already in its argv. None of those callers can
// see the others, so the recent-files list has to be fed from here. The
// router stays a transport: it interprets nothing, it only reports the fact
// as an app_msg {kind:"open.routed", path, app_id} on the session app's
// event channel — From is unset, which is what marks it router-originated
// (a cross-app forgery would arrive with an attested From).

// openRoutedKind is the app_msg kind the session app subscribes to.
const openRoutedKind = "open.routed"

// sessionInstance returns the live desktop-surface instance, or nil while no
// session app is attached (kiosk / --no-session / mid-restart).
func (r *Router) sessionInstance() *AppInstance {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, inst := range r.apps {
		if inst.Manifest == nil {
			continue
		}
		if inst.Manifest.Surface == SurfaceDesktop ||
			(r.cfg.SessionAppID != "" && inst.Manifest.ID == r.cfg.SessionAppID) {
			return inst
		}
	}
	return nil
}

// noteOpenRouted records that appID was launched to open path. via names the
// route for the log line ("open.request", "spawn.request", "attach"). Best
// effort: with no session app attached the notice is dropped, not queued —
// the list is a convenience, not a ledger.
func (r *Router) noteOpenRouted(path, appID, via string) {
	if path == "" || appID == "" {
		return
	}
	r.log("open.routed: path=%q app=%s via=%s", path, appID, via)
	sess := r.sessionInstance()
	if sess == nil {
		return
	}
	msg := wire.NewEvtAppMsg(sess.WindowID, map[string]any{
		"kind":   openRoutedKind,
		"path":   path,
		"app_id": appID,
	})
	if err := sess.WriteEvt(msg); err != nil {
		r.log("open.routed: notify session instance=%s: %v", sess.InstanceID, err)
	}
}

// openPathFromArgv returns the value following the first `--open` in argv,
// or "" — the same parse Conn.LaunchOpenPath does app-side, applied to a
// process the router did not spawn.
func openPathFromArgv(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--open" {
			return argv[i+1]
		}
	}
	return ""
}

// procOpenPath reads /proc/<pid>/cmdline for a fresh-attached app and
// returns its `--open` path, if any. Unreadable (root-owned child, pid
// gone) → "" — the attach itself is not affected.
func procOpenPath(pid int) string {
	if pid <= 0 {
		return ""
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	parts := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
	argv := make([]string, 0, len(parts))
	for _, p := range parts {
		argv = append(argv, string(p))
	}
	return openPathFromArgv(argv)
}
