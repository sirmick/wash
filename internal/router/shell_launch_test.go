package router

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// logCapture is a thread-safe sink for the router's injectable logger so
// a test can assert on what handleLaunch logged — its only observable
// output, since launch is fire-and-forget with no response frame.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) log(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *logCapture) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// TestShellLaunchRejects drives the shell.launch ctrl verb (docs/REMOTE.md
// §6.1) for the cases that must NOT spawn: an unknown app id and a
// desktop-surface app. Both are fire-and-forget refusals whose only trace
// is a router log line, so we assert on the captured log. (The happy path
// — spawning a real binary — is covered by the remote-apps e2e.)
func TestShellLaunchRejects(t *testing.T) {
	lc := &logCapture{}
	reg := NewRegistry()
	// A desktop-surface entry: enabled (manifest + no reason), but the
	// autoboot session owns the desktop, so launching it via the shell
	// must be refused before any spawn attempt.
	reg.RegisterEntry(&Entry{Path: "/unused/wash-session", Manifest: &Manifest{
		ID:              "com.wash.session",
		Name:            "Session",
		Version:         "0.9.0",
		ProtocolVersion: ProtocolVersion,
		Element:         "wash-app-session",
		Surface:         SurfaceDesktop,
		Instancing:      InstancingSingleton,
	}})
	r := NewRouter(Config{}, reg, lc.log)

	shellPair := wiretest.NewPipePair()
	shellDone := make(chan struct{})
	go func() {
		defer close(shellDone)
		_ = r.HandleShell(context.Background(), shellPair.EndA())
	}()

	// Drain the catalog the router sends on connect.
	if _, ok := readCtrl(t, shellPair.EndB()).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	}

	writeCtrl(t, shellPair.EndB(), wire.NewShellLaunch("com.wash.nonesuch"))
	writeCtrl(t, shellPair.EndB(), wire.NewShellLaunch("com.wash.session"))

	// handleLaunch logs synchronously for both refusal paths (no spawn
	// goroutine is reached), but the shell read loop is concurrent, so
	// poll briefly for both lines.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if lc.contains("shell launch com.wash.nonesuch: unknown or disabled app") &&
			lc.contains("shell launch com.wash.session: refusing desktop-surface app") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing refusal log line(s); got %v", lc.lines)
		}
		time.Sleep(5 * time.Millisecond)
	}

	shellPair.Close()
	waitClose(t, shellDone)
}
