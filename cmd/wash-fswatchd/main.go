// Command wash-fswatchd is the remote (B-side) watch daemon for wash-to-wash
// mounts. It runs on the wash host, watches its local filesystem with inotify,
// and streams change events over stdin/stdout to the mounting host (A), which
// turns them into a local fswatch.Feed. It is launched the same way the remote
// router is — `ssh <host> wash-fswatchd` — so the watch channel rides ssh on
// the shared agent, no extra auth.
//
// This is the "wash channel" half of the mount; SFTP carries the bytes.
package main

import (
	"log"
	"os"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/remotewatch"
)

// stdio adapts the process's stdin/stdout into a single io.ReadWriteCloser — the
// transport the ssh session hands us.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }

func main() {
	log.SetFlags(0)
	log.SetPrefix("wash-fswatchd: ")
	// Diagnostics go to stderr; stdout is the event wire and must stay clean.
	log.SetOutput(os.Stderr)

	mgr, err := fswatch.New()
	if err != nil {
		log.Fatalf("inotify: %v", err)
	}
	defer mgr.Close()

	if err := remotewatch.Serve(stdio{}, fswatch.LocalSource(mgr)); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
