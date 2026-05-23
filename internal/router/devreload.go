// Dev-mode auto-reload. When cfg.Dev is set, the router watches:
//   - cfg.AppsDirs for binary changes (CREATE / WRITE / RENAME)
//   - the wash-router binary itself
//
// On an app binary change, every running instance of THAT app is
// killed (the existing cleanup goroutine in spawnAndRun handles
// unregister + destroy-window) and a ShellReload is broadcast so
// every connected browser tab refreshes — the embedded asset
// bundles are stale and customElements is one-shot per tab.
//
// On a router-binary change, the router shuts down its listeners
// gracefully then syscall.Exec's itself with the same argv/env.
// The shell's WS drops and the browser auto-reconnects when the
// new instance comes up.
//
// Production builds leave cfg.Dev=false; the watcher goroutine
// never starts. There's no runtime cost when disabled.

package router

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/sirmick/wash/internal/wire"
)

// devReloadDebounce is the window during which repeated
// fsnotify events for the same file collapse into one action.
// `go build` writes the binary via temp-file + rename, which
// shows up as several events in quick succession.
const devReloadDebounce = 250 * time.Millisecond

// StartDevReload spawns the watcher goroutine. Caller passes the
// resolved path of the router binary so we don't have to redo
// the os.Executable + EvalSymlinks dance in two places. Safe to
// call with an empty routerExe (skips self-reload).
func (r *Router) StartDevReload(routerExe string) {
	if !r.cfg.Dev {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		r.log("dev-reload: watcher unavailable: %v", err)
		return
	}
	// Watch each apps dir (events fire for entries created /
	// modified inside, which is what `go build -o out/wash-fm`
	// produces). Also watch the parent dir of the router binary
	// so renames-in-place register.
	for _, d := range r.cfg.AppsDirs {
		if err := w.Add(d); err != nil {
			r.log("dev-reload: watch %s: %v", d, err)
		}
	}
	if routerExe != "" {
		if err := w.Add(filepath.Dir(routerExe)); err != nil {
			r.log("dev-reload: watch %s: %v", filepath.Dir(routerExe), err)
		}
	}
	r.log("dev-reload: watching apps + router binary; auto-reload enabled")

	go r.runDevReload(w, routerExe)
}

func (r *Router) runDevReload(w *fsnotify.Watcher, routerExe string) {
	defer w.Close()

	var mu sync.Mutex
	pending := map[string]*time.Timer{}
	fire := func(path string) {
		r.handleBinaryChange(path, routerExe)
	}
	debounce := func(path string) {
		mu.Lock()
		defer mu.Unlock()
		if t, ok := pending[path]; ok {
			t.Stop()
		}
		pending[path] = time.AfterFunc(devReloadDebounce, func() {
			mu.Lock()
			delete(pending, path)
			mu.Unlock()
			fire(path)
		})
	}
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// We only care about events that produce a fully-
			// written binary. Create + Write + Rename cover the
			// `go build` patterns (temp-then-rename, or
			// in-place overwrite). Chmod alone is ignored.
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			// Skip transient go-build artefacts (e.g.
			// wash-fm-go-tmp-umask) and unrelated files. Only
			// registered binaries + the router itself trigger.
			if !r.isWatchedBinary(ev.Name, routerExe) {
				continue
			}
			debounce(ev.Name)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			r.log("dev-reload: watcher error: %v", err)
		}
	}
}

// handleBinaryChange reacts to a single binary-change event. The
// router-binary path triggers self-reexec; any other path is
// treated as an app binary, and the matching instances are
// killed + browsers are told to reload.
func (r *Router) handleBinaryChange(path, routerExe string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	if routerExe != "" && resolved == routerExe {
		r.log("dev-reload: router binary changed (%s) — re-exec", path)
		r.broadcastReload("router binary changed")
		// Brief pause so the shell.reload frame actually flushes
		// before we kill the listeners.
		time.Sleep(50 * time.Millisecond)
		r.execSelf(routerExe)
		return
	}
	matched := r.killInstancesByBinary(resolved)
	if matched == 0 {
		// Binary changed but no instances are running. Still
		// reload — the user's tab has stale catalog state, and
		// a freshly-opened window should fetch the new bundle.
		r.log("dev-reload: %s changed (no live instances)", filepath.Base(path))
	} else {
		r.log("dev-reload: %s changed → killed %d instance(s)", filepath.Base(path), matched)
	}
	r.broadcastReload("app binary changed: " + filepath.Base(path))
}

// isWatchedBinary reports whether `path` is either the router
// binary itself or a registered app binary. Filters out transient
// go-build temp files, .stamp markers, and unrelated dir
// contents.
func (r *Router) isWatchedBinary(path, routerExe string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// File may have been moved-away in mid-build — keep the
		// raw path so debounce + the later re-resolve has a
		// chance to match.
		resolved = path
	}
	if routerExe != "" && resolved == routerExe {
		return true
	}
	for _, e := range r.reg.Entries() {
		regResolved, err := filepath.EvalSymlinks(e.Path)
		if err != nil {
			regResolved = e.Path
		}
		if regResolved == resolved {
			return true
		}
	}
	return false
}

// killInstancesByBinary terminates every AppInstance whose
// registered Manifest path resolves to the given binary path.
// Returns the count for logging. The existing cleanup goroutine
// in spawnAndRun handles unregister + window destruction.
func (r *Router) killInstancesByBinary(binaryPath string) int {
	r.mu.Lock()
	victims := []*AppInstance{}
	for _, inst := range r.apps {
		entry := r.reg.ByID(inst.AppID)
		if entry == nil {
			continue
		}
		regResolved, err := filepath.EvalSymlinks(entry.Path)
		if err != nil {
			regResolved = entry.Path
		}
		if regResolved == binaryPath {
			victims = append(victims, inst)
		}
	}
	r.mu.Unlock()
	for _, inst := range victims {
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.expectedExit.Store(true)
			_ = inst.Cmd.Process.Signal(syscall.SIGTERM)
		} else {
			// Fresh-attach apps don't have a Cmd we own; closing
			// the transport drops the conn and the loop exits.
			_ = inst.Transport.Close()
		}
	}
	return len(victims)
}

// broadcastReload pushes a shell.reload frame to every connected
// browser. Best-effort: a busy/closed shell just times out and
// the new one (post-reload) reconnects.
func (r *Router) broadcastReload(reason string) {
	msg := wire.NewShellReload(reason)
	for _, s := range r.shellList() {
		_ = s.WriteCtrl(msg)
	}
}

// execSelf syscall.Execs the router binary with the same argv +
// env. Go's net.Listen sets FD_CLOEXEC on listener fds, so the
// kernel closes them as part of exec — the new instance rebinds
// from scratch. App connections die the same way; their children
// see EOF on the wash socket and exit. Browsers auto-reconnect
// when the new instance starts accepting.
func (r *Router) execSelf(routerExe string) {
	argv := append([]string{routerExe}, os.Args[1:]...)
	if err := syscall.Exec(routerExe, argv, os.Environ()); err != nil {
		r.log("dev-reload: re-exec failed: %v", err)
		os.Exit(1)
	}
}
