package routerrun

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/router"
)

// startRelay runs an in-process stand-in for the detached relay router:
// the raw listener plus its keep socket, wired exactly as Run wires them.
// done closes once the listener has returned.
func startRelay(t *testing.T, sock string) (cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	r := router.NewRouter(router.Config{NoSession: true}, router.NewRegistry(), func(f string, a ...any) {})
	ctx, cancelFn := context.WithCancel(context.Background())
	d := make(chan struct{})
	logf := func(f string, a ...any) { t.Logf("relay: "+f, a...) }
	go func() {
		defer close(d)
		_ = runRawListener(ctx, r, "unix", sock, logf, func() {
			if err := serveKeepSocket(ctx, r, sock+keepSuffix, cancelFn, logf); err != nil {
				t.Errorf("keep socket: %v", err)
			}
		})
	}()
	t.Cleanup(func() {
		cancelFn()
		<-d
	})
	return cancelFn, d
}

func relaySock(t *testing.T) string {
	// Short: unix socket paths cap at ~108 bytes and t.TempDir() is long.
	dir, err := os.MkdirTemp("", "wr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "r.sock")
}

// TestEnsureRelayRouterReusesLiveRouter: a keeper arriving after an ssh
// drop finds the router the drop left running and attaches to it without
// starting a second one.
func TestEnsureRelayRouterReusesLiveRouter(t *testing.T) {
	sock := relaySock(t)
	startRelay(t, sock)
	waitDial(t, sock)

	conn, reused, err := ensureRelayRouter(sock, func() (<-chan struct{}, error) {
		t.Fatal("spawned a second relay router while one was serving")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !reused {
		t.Error("reused = false for a live router")
	}
}

// TestEnsureRelayRouterSpawnsOnce: keepers racing on a cold socket start
// exactly one router (the flock + re-check), and all of them attach to it.
func TestEnsureRelayRouterSpawnsOnce(t *testing.T) {
	sock := relaySock(t)
	var spawns atomic.Int32
	spawn := func() (<-chan struct{}, error) {
		spawns.Add(1)
		startRelay(t, sock)
		return make(chan struct{}), nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, err := ensureRelayRouter(sock, spawn)
			if err != nil {
				errs <- err
				return
			}
			c.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := spawns.Load(); n != 1 {
		t.Errorf("spawned %d relay routers, want 1", n)
	}
}

// TestEnsureRelayRouterReportsEarlyExit: a router that dies during start-up
// fails the keeper promptly (pointing at its log) rather than after the
// full readiness timeout.
func TestEnsureRelayRouterReportsEarlyExit(t *testing.T) {
	sock := relaySock(t)
	start := time.Now()
	_, _, err := ensureRelayRouter(sock, func() (<-chan struct{}, error) {
		dead := make(chan struct{})
		close(dead)
		return dead, nil
	})
	if err == nil || !strings.Contains(err.Error(), logSuffix) {
		t.Fatalf("err = %v, want an exited-during-start-up error naming the log", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s to notice the dead router", time.Since(start))
	}
}

// TestKeeperStdinEOFLeavesRouterRunning: the ssh session ending (stdin
// EOF) ends only the keeper — the router keeps serving for the next one.
func TestKeeperStdinEOFLeavesRouterRunning(t *testing.T) {
	sock := relaySock(t)
	_, done := startRelay(t, sock)
	conn := waitDial(t, sock)
	if code := keepUntil(conn, strings.NewReader(""), io.Discard); code != 0 {
		t.Errorf("keeper exit = %d on stdin EOF, want 0", code)
	}
	conn.Close()
	select {
	case <-done:
		t.Fatal("router exited when its keeper's ssh session ended")
	case <-time.After(200 * time.Millisecond):
	}
	c2 := waitDial(t, sock) // a later keeper still finds it
	c2.Close()
}

// TestKeeperStopShutsRouterDown: a deliberate disconnect ("stop" on stdin)
// is relayed and the router exits instead of lingering.
func TestKeeperStopShutsRouterDown(t *testing.T) {
	sock := relaySock(t)
	_, done := startRelay(t, sock)
	conn := waitDial(t, sock)
	defer conn.Close()
	if code := keepUntil(conn, strings.NewReader("stop\n"), io.Discard); code != 0 {
		t.Errorf("keeper exit = %d on stop, want 0", code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("router did not exit on stop")
	}
}

// TestKeeperExitsWhenRouterDies: the router going away (reaped, crashed)
// ends the keeper non-zero, so A's ssh ends and the supervisor re-dials.
func TestKeeperExitsWhenRouterDies(t *testing.T) {
	sock := relaySock(t)
	cancel, _ := startRelay(t, sock)
	conn := waitDial(t, sock)
	defer conn.Close()
	pr, pw := io.Pipe() // stdin that never closes
	defer pw.Close()
	res := make(chan int, 1)
	go func() { res <- keepUntil(conn, pr, io.Discard) }()
	cancel()
	select {
	case code := <-res:
		if code != 1 {
			t.Errorf("keeper exit = %d when the router died, want 1", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("keeper did not notice the router exit")
	}
}

// TestDetachedArgv: the detached router runs this invocation minus
// --detach, with a log next to the socket, re-entering the router through
// the multi-call binary when that is how we were started.
func TestDetachedArgv(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()

	in := []string{"--listen-raw", "unix:/tmp/r.sock", "--detach", "--idle-timeout", "15m"}
	os.Args = append([]string{"/usr/bin/wash-router"}, in...)
	got := strings.Join(detachedArgv(in, "/tmp/r.sock"), " ")
	if want := "/usr/bin/wash-router --listen-raw unix:/tmp/r.sock --idle-timeout 15m --log-file /tmp/r.sock.log"; got != want {
		t.Errorf("argv = %q\n want %q", got, want)
	}

	in = append(in, "--log-file=/x.log")
	os.Args = append([]string{"wash", "router"}, in...)
	got = strings.Join(detachedArgv(in, "/tmp/r.sock"), " ")
	if want := "wash router --listen-raw unix:/tmp/r.sock --idle-timeout 15m --log-file=/x.log"; got != want {
		t.Errorf("multi-call argv = %q\n want %q", got, want)
	}
}

func waitDial(t *testing.T, sock string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := dialOwnRelay(sock)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
