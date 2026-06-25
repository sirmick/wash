package sdk

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Terminate hooks: cleanup that must run when the router stops this app.
//
// The router stops apps with SIGTERM first (internal/router/signal.go),
// escalating to SIGKILL only after a grace period. With no handler the OS
// terminates the process via SIGTERM's default action, which runs neither
// Go's deferred funcs nor any app cleanup. That's fine for a stateless app —
// the OS reaps it — but a service that spawns a *child* process tree (e.g.
// wash-vscode → code-server, wash-display → the compositor) needs to kill
// that tree itself, or the children orphan to PID 1 and leak (Pdeathsig only
// covers the direct child, not its grandchildren).
//
// OnTerminate registers a cleanup func and lazily installs a single
// SIGINT/SIGTERM handler that runs every registered hook (LIFO, panic-guarded)
// and then exits 0. The handler is installed only once, and only for apps
// that actually register a hook — apps that don't keep the old "OS tears the
// process down with zero SDK involvement" behaviour.
//
// Exit 0 is deliberate: a router-driven SIGTERM is an orderly stop, not a
// crash (see router.go's maybeBroadcastCrash). os.Exit also runs the runtime's
// registered -cover exit hook, so flushing coverage counters on an
// instrumented run is a side effect of this same path (it subsumes the old
// coverage-only handler).
var (
	termMu        sync.Mutex
	termHooks     []func()
	termInstalled bool
)

// OnTerminate registers fn to run when the app receives SIGINT/SIGTERM,
// then installs the signal handler if it isn't already. A nil fn registers
// no hook but still ensures the handler is installed (used to keep the
// coverage-flush exit on instrumented runs that have no app cleanup).
func OnTerminate(fn func()) {
	termMu.Lock()
	defer termMu.Unlock()
	if fn != nil {
		termHooks = append(termHooks, fn)
	}
	if termInstalled {
		return
	}
	termInstalled = true
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		runTerminateHooks()
		os.Exit(0)
	}()
}

// runTerminateHooks runs the registered hooks newest-first. Split out so it
// can be exercised in tests without the signal/os.Exit. A panicking hook is
// contained so it can't stop the others (or wedge the shutdown).
func runTerminateHooks() {
	termMu.Lock()
	hooks := make([]func(), len(termHooks))
	copy(hooks, termHooks)
	termMu.Unlock()
	for i := len(hooks) - 1; i >= 0; i-- {
		func(h func()) {
			defer func() { _ = recover() }()
			h()
		}(hooks[i])
	}
}

// installCoverageFlushOnSignal preserves the old call site in Main: on a
// coverage run (GOCOVERDIR set) ensure the terminate handler exists so its
// os.Exit flushes -cover counters, even when no app registered a hook.
func installCoverageFlushOnSignal() {
	if os.Getenv("GOCOVERDIR") == "" {
		return
	}
	OnTerminate(nil)
}
