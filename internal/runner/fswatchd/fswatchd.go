// Package fswatchd is the runnable core of wash-fswatchd, the B-side watch
// daemon for wash-to-wash mounts. Factored out of cmd/wash-fswatchd so the
// multicall `wash` binary can dispatch to it too (argv[0] == "wash-fswatchd"),
// the same way it dispatches wash-router.
package fswatchd

import (
	"log"
	"os"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/remotewatch"
)

// stdio adapts the process's stdin/stdout into one io.ReadWriteCloser — the
// transport the ssh session hands the daemon.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }

// Run watches the local filesystem with inotify and streams change events over
// stdin/stdout. Diagnostics go to stderr so stdout stays a clean event wire.
// Returns a process exit code.
func Run(_ []string) int {
	log.SetFlags(0)
	log.SetPrefix("wash-fswatchd: ")
	log.SetOutput(os.Stderr)

	mgr, err := fswatch.New()
	if err != nil {
		log.Printf("inotify: %v", err)
		return 1
	}
	defer mgr.Close()

	if err := remotewatch.Serve(stdio{}, fswatch.LocalSource(mgr)); err != nil {
		log.Printf("serve: %v", err)
		return 1
	}
	return 0
}
