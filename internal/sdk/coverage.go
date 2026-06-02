package sdk

import (
	"os"
	"os/signal"
	"syscall"
)

// installCoverageFlushOnSignal makes coverage-instrumented app binaries
// (built with `go build -cover`, i.e. ./test.sh --coverage) write their
// counters before the process goes away.
//
// The router stops apps with SIGTERM (internal/router/signal.go). With no
// handler the OS terminates the process via SIGTERM's default action,
// which does NOT run Go's coverage exit hook — so an instrumented app
// spawned under the e2e suite would write nothing to GOCOVERDIR and its
// backend coverage would read as zero. Exiting via os.Exit instead DOES
// run the exit hook (it flushes covmeta/covcounters into GOCOVERDIR),
// which is how we capture how much of each app BE the e2e suite exercises.
//
// This is gated entirely on GOCOVERDIR being set, which only happens under
// a coverage build/run. In normal operation (and every non-coverage test)
// GOCOVERDIR is empty, so NO handler is installed and SIGTERM behaviour is
// exactly as before: the OS tears the process down with zero SDK
// involvement. The handler simply does not exist outside a coverage run.
func installCoverageFlushOnSignal() {
	if os.Getenv("GOCOVERDIR") == "" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		// os.Exit runs the runtime's registered coverage exit hook,
		// flushing counters to GOCOVERDIR. Exit 0: a router-driven
		// SIGTERM is an orderly stop, not a crash (see router.go's
		// maybeBroadcastCrash).
		os.Exit(0)
	}()
}
