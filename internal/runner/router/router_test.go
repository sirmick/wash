package routerrun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/router"
	"github.com/sirmick/wash/pkg/wire"
)

func TestParseTransport(t *testing.T) {
	cases := []struct {
		in         string
		wantScheme string
		wantPath   string
		wantErr    string // substring; empty = no error expected
	}{
		// Defaults: empty or "ws".
		{"", "ws", "", ""},
		{"ws", "ws", "", ""},

		// Valid non-ws transports.
		{"virtio-console:/dev/vport0p2", "virtio-console", "/dev/vport0p2", ""},
		{"serial:/dev/ttyS2", "serial", "/dev/ttyS2", ""},

		// Errors.
		{"virtio-console", "", "", "expected <scheme>:<path>"},
		{"virtio-console:", "", "", "path is empty"},
		{"serial:", "", "", "path is empty"},
		{"http:/foo", "", "", "unsupported scheme"},
		{"ws:something", "", "", "ws scheme does not take a path"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			scheme, path, err := parseTransport(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("got no error, want %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got err %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if scheme != tc.wantScheme {
				t.Fatalf("scheme=%q, want %q", scheme, tc.wantScheme)
			}
			if path != tc.wantPath {
				t.Fatalf("path=%q, want %q", path, tc.wantPath)
			}
		})
	}
}

// TestWriteReadyToken — token format is stable; outer JS watchers
// match the exact "WASH_READY\n" string.
func TestWriteReadyToken(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready")
	// Pre-create with O_APPEND mode so writeReadyToken can open it.
	f, err := os.Create(target)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close()

	if err := writeReadyToken(target); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "WASH_READY\n" {
		t.Fatalf("ready token = %q, want %q", got, "WASH_READY\n")
	}

	// Second call appends a second token (no truncation), proving
	// the O_APPEND mode behaves as documented.
	if err := writeReadyToken(target); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if string(got) != "WASH_READY\nWASH_READY\n" {
		t.Fatalf("after second write = %q, want WASH_READY\\nWASH_READY\\n", got)
	}
}

// TestRunShellOverStreamRouterHandshake exercises the full path —
// open a FIFO-like file (an os.Pipe one-direction stand-in won't do;
// we need bidirectional, so two pipes), wrap as wash StreamTransport,
// confirm the router reads framed JSON on it and writes its catalog
// frame back.
//
// We're not testing HandleShell's full behavior (that's the loopback
// spine test's job); we're testing that `runShellOverStream` correctly
// hooks the byte stream into HandleShell + writes the ready token.
func TestRunShellOverStreamRouterHandshake(t *testing.T) {
	// Build a router with an empty registry (no apps spawned).
	reg := router.NewRegistry()
	r := router.NewRouter(router.Config{NoSession: true}, reg, func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	// Create a bidirectional pipe by pairing two os.Pipe's. The
	// router holds one end (read from peer-writer, write to
	// peer-reader); the test holds the other.
	routerR, peerW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe1: %v", err)
	}
	peerR, routerW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe2: %v", err)
	}
	t.Cleanup(func() {
		routerR.Close()
		routerW.Close()
		peerR.Close()
		peerW.Close()
	})

	transport := wire.NewStreamTransport(&pipeRWC{r: routerR, w: routerW})

	// Ready-token file in a temp dir.
	readyPath := filepath.Join(t.TempDir(), "ready")
	rf, err := os.Create(readyPath)
	if err != nil {
		t.Fatalf("create ready: %v", err)
	}
	rf.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start HandleShell on the transport in the background. We can't
	// reuse runShellOverStream directly because it opens a path; we
	// invoke the equivalent here on the in-memory pair.
	done := make(chan error, 1)
	go func() {
		if err := writeReadyToken(readyPath); err != nil {
			t.Logf("ready write: %v", err)
		}
		done <- r.HandleShell(ctx, transport)
	}()

	// Drain one frame off the peer end to prove the router is
	// actively writing on the byte stream — that's "the handshake
	// is reachable." The first frame the router sends is
	// ShellCatalog (an empty catalog in this configuration).
	peerStream := wire.NewStreamTransport(&pipeRWC{r: peerR, w: peerW})
	type readResult struct {
		f   wire.Frame
		err error
	}
	resCh := make(chan readResult, 1)
	go func() {
		f, err := peerStream.ReadFrame()
		resCh <- readResult{f, err}
	}()
	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("read first frame: %v", res.err)
		}
		if res.f.Channel != 0 {
			t.Fatalf("expected channel 0 (control), got %d", res.f.Channel)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame from router within 2s")
	}

	// Ready token must be present.
	got, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatalf("read ready: %v", err)
	}
	if string(got) != "WASH_READY\n" {
		t.Fatalf("ready token = %q, want WASH_READY", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleShell did not exit on ctx cancel")
	}
}

// pipeRWC pairs an os.Pipe read end with a writer end into one
// io.ReadWriteCloser so wire.StreamTransport can consume it. Closing
// closes both halves.
type pipeRWC struct {
	r *os.File
	w *os.File
	// once guards Close so a double-close (test cleanup + HandleShell
	// teardown) doesn't error on the second call.
	once sync.Once
}

func (p *pipeRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeRWC) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeRWC) Close() error {
	var err error
	p.once.Do(func() {
		errR := p.r.Close()
		errW := p.w.Close()
		if errR != nil {
			err = errR
		} else if errW != nil {
			err = errW
		}
	})
	if err == io.EOF {
		return nil
	}
	return err
}
