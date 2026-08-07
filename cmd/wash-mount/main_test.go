package main

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sirmick/wash/internal/sftptest"
	"github.com/sirmick/wash/internal/washmount"
)

// blackholeProxy is a TCP relay that can SILENTLY sever the connections open at
// the moment Sever() is called: it keeps reading their bytes (so the peer's TCP
// send buffer never fills and no error is raised) but forwards nothing and never
// closes the socket. That is a link that has gone quiet — a NAT/conntrack entry
// dropped, a suspended laptop, a pulled cable — the case where NOTHING errors,
// which is precisely what a FIN-sending close (curSSH.Close, pkill) does not
// model. Connections opened AFTER Sever() are healthy, so a re-dial recovers —
// mirroring how a fresh 5-tuple gets a new conntrack entry.
type blackholeProxy struct {
	ln       net.Listener
	upstream string

	mu    sync.Mutex
	conns []*bhConn
}

type bhConn struct {
	mu   sync.Mutex
	dead bool
}

func (c *bhConn) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

func newBlackholeProxy(t *testing.T, upstream string) *blackholeProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &blackholeProxy{ln: ln, upstream: upstream}
	go p.serve()
	return p
}

func (p *blackholeProxy) Addr() string { return p.ln.Addr().String() }
func (p *blackholeProxy) Close()       { p.ln.Close() }

// Sever black-holes every currently-open connection. New ones stay healthy.
func (p *blackholeProxy) Sever() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.mu.Lock()
		c.dead = true
		c.mu.Unlock()
	}
}

func (p *blackholeProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(client)
	}
}

func (p *blackholeProxy) handle(client net.Conn) {
	up, err := net.Dial("tcp", p.upstream)
	if err != nil {
		client.Close()
		return
	}
	c := &bhConn{}
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()

	done := make(chan struct{}, 2)
	go func() { p.pipe(up, client, c); done <- struct{}{} }()
	go func() { p.pipe(client, up, c); done <- struct{}{} }()
	<-done
	client.Close()
	up.Close()
}

// pipe copies src→dst until either end errors, but once the connection is
// severed it consumes and discards src's bytes without forwarding them and
// without closing — a black hole, not a reset.
func (p *blackholeProxy) pipe(dst, src net.Conn, c *bhConn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && !c.isDead() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// TestKeepaliveDetectsSilentPeer is the isolated proof of the new keepalive: it
// must (a) leave a healthy connection alone and (b) close a connection whose
// peer has gone silent — the signal that lets washmount re-dial instead of
// parking a request in the ssh transport until the kernel TCP timeout. Runs
// without FUSE, so it always executes in CI.
func TestKeepaliveDetectsSilentPeer(t *testing.T) {
	const interval = 200 * time.Millisecond

	addr, stop, err := sftptest.NewServer()
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	defer stop()
	proxy := newBlackholeProxy(t, addr)
	defer proxy.Close()

	sc, sshConn, err := sftptest.Dial(proxy.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sc.Close()
	go keepalive(sshConn, interval)

	// (a) A healthy peer answers pings — the connection survives several
	// intervals. NewSession succeeding proves the transport is live.
	time.Sleep(5 * interval)
	if sess, err := sshConn.NewSession(); err != nil {
		t.Fatalf("healthy connection was closed by keepalive: %v", err)
	} else {
		sess.Close()
	}

	// (b) Silence the peer. Within a couple of intervals the unanswered ping
	// must trip keepalive into closing the connection.
	proxy.Sever()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := sshConn.NewSession(); err != nil {
			break // transport closed → keepalive detected the silent peer
		}
		if time.Now().After(deadline) {
			t.Fatal("keepalive did not close the connection after the peer went silent")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestMountRecoversFromSilentLinkDeath is the end-to-end silent-death gate for
// the wash-mount CLI: a real FUSE mount whose SFTP link goes QUIET (not a clean
// close) must self-heal — the per-op timeout abandons the parked request, the
// owned client is dropped, and the next op re-dials a fresh connection. Before
// the fix the CLI used a fixed, borrowed client that could never be dropped, so
// this exact case leaked a concurrency slot per hung op and wedged the mount.
func TestMountRecoversFromSilentLinkDeath(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("no /dev/fuse: %v", err)
	}
	backing := t.TempDir()
	mnt := t.TempDir()
	if err := os.WriteFile(filepath.Join(backing, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	addr, stopServer, err := sftptest.NewServer()
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	defer stopServer()
	proxy := newBlackholeProxy(t, addr)
	defer proxy.Close()

	// Drive the CLI's real dialer, but connect through the proxy with the test
	// key (no ssh agent / known_hosts in a unit test).
	var dials int32
	d := &sshDialer{
		keepaliveEvery: 500 * time.Millisecond,
		connect: func() (*ssh.Client, error) {
			c, err := ssh.Dial("tcp", proxy.Addr(), &ssh.ClientConfig{
				User:            "test",
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Timeout:         5 * time.Second,
			})
			if err == nil {
				atomic.AddInt32(&dials, 1)
			}
			return c, err
		},
	}

	server, err := washmount.MountWithDialer(d.dial, washmount.Options{
		MountPoint: mnt,
		RemoteRoot: backing,
		OpTimeout:  2 * time.Second,
	})
	if err != nil {
		t.Skipf("mount failed (FUSE unavailable?): %v", err)
	}
	t.Cleanup(func() {
		washmount.Unmount(server, mnt)
		d.close()
	})

	// Works before the outage.
	if b, err := os.ReadFile(filepath.Join(mnt, "a.txt")); err != nil || string(b) != "v1" {
		t.Fatalf("pre-outage read = %q, %v; want v1", b, err)
	}

	// A NEW file the kernel can't serve from cache — reaching it must hit the
	// backend and force a re-dial once the link is gone.
	if err := os.WriteFile(filepath.Join(backing, "after.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Silently sever the live connection: no FIN, no RST — the request in flight
	// simply stops getting answers.
	proxy.Sever()

	deadline := time.Now().Add(30 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(mnt, "after.txt")); err == nil && string(b) == "v2" {
			recovered = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("mount did not recover after a silent link death (wedged)")
	}
	if got := atomic.LoadInt32(&dials); got < 2 {
		t.Fatalf("expected a re-dial after the outage, got %d dials", got)
	}
}
