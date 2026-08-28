// Command wash-mount mounts a remote machine's filesystem locally over SFTP as
// a real kernel FUSE mount, so every process on the box sees it.
//
// It is an OPTIONAL wash component: it depends on the FUSE kernel module and
// fusermount3, which are not present everywhere (notably the in-browser VM has
// no FUSE). It is built and packaged opt-in, the same way wash-display is.
//
// Usage:
//
//	wash-mount [flags] [user@]host[:port]:/remote/path  /local/mountpoint
//
// Authentication is via the SSH agent ($SSH_AUTH_SOCK), matching wash-remote.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/sirmick/wash/internal/washmount"
	"github.com/sirmick/wash/pkg/wire"
)

func main() {
	// Not an app: decline discovery's probe before flag parsing, which
	// would otherwise dump this whole usage block into the router's boot
	// log (pkg/wire.DeclineManifestProbe).
	wire.DeclineManifestProbe(os.Args[1:])

	log.SetFlags(0)
	log.SetPrefix("wash-mount: ")

	timeout := flag.Duration("timeout", 30*time.Second, "per-operation backend timeout")
	allowOther := flag.Bool("allow-other", false, "expose the mount to other users (needs user_allow_other or root)")
	insecure := flag.Bool("insecure-host-key", false, "skip host key verification (testing only)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: wash-mount [flags] [user@]host[:port]:/remote/path /local/mountpoint\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}

	user, host, remotePath, err := parseTarget(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	mountpoint := flag.Arg(1)

	// A reconnecting, owned dialer: MountWithDialer re-dials on a drop instead of
	// handing back the same dead client forever (which a fixed-client Mount does
	// — it can only EIO), and because the client is owned the FUSE layer closes
	// it on a drop, unblocking any op parked in the sftp transport. Paired with
	// the per-connection keepalive below, a silent link death recovers rather
	// than wedging the mount.
	d := &sshDialer{user: user, host: host, insecure: *insecure}
	server, err := washmount.MountWithDialer(d.dial, washmount.Options{
		MountPoint: mountpoint,
		RemoteRoot: remotePath,
		OpTimeout:  *timeout,
		AllowOther: *allowOther,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer d.close()
	log.Printf("mounted %s:%s at %s", host, remotePath, mountpoint)

	// Unmount cleanly on signal so we never leave a wedged mountpoint behind.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("unmounting")
		// Escalating unmount: never leave a wedged mountpoint behind even if the
		// backend has gone unresponsive.
		if err := washmount.Unmount(server, mountpoint); err != nil {
			log.Printf("unmount: %v", err)
		}
	}()
	server.Wait()
}

// sshDialer establishes an SFTP client for washmount, reconnecting on demand.
// Each dial closes the previous ssh connection (so a re-dial after a drop doesn't
// leak the dead one) and starts a keepalive so a silent link death surfaces as an
// error promptly instead of blocking forever in the sftp transport.
type sshDialer struct {
	user, host string
	insecure   bool

	// connect establishes the ssh transport; nil selects the agent-based dial.
	// A test seam so the reconnect/keepalive behaviour can be exercised against
	// an in-process server without an ssh agent or known_hosts.
	connect func() (*ssh.Client, error)
	// keepaliveEvery is the ping period; zero selects the default. Overridable
	// so tests don't wait real seconds.
	keepaliveEvery time.Duration

	mu   sync.Mutex
	prev *ssh.Client // the connection behind the last-returned client
}

func (d *sshDialer) keepaliveInterval() time.Duration {
	if d.keepaliveEvery > 0 {
		return d.keepaliveEvery
	}
	return 15 * time.Second
}

func (d *sshDialer) sshConnect() (*ssh.Client, error) {
	if d.connect != nil {
		return d.connect()
	}
	return dial(d.user, d.host, d.insecure)
}

func (d *sshDialer) dial() (*sftp.Client, error) {
	sshClient, err := d.sshConnect()
	if err != nil {
		return nil, err
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, err
	}
	go keepalive(sshClient, d.keepaliveInterval())

	d.mu.Lock()
	prev := d.prev
	d.prev = sshClient
	d.mu.Unlock()
	if prev != nil {
		prev.Close() // drop the superseded connection (and stop its keepalive)
	}
	return client, nil
}

// close tears down the current connection on unmount.
func (d *sshDialer) close() {
	d.mu.Lock()
	prev := d.prev
	d.prev = nil
	d.mu.Unlock()
	if prev != nil {
		prev.Close()
	}
}

// keepalive pings the server every interval and, if a ping isn't answered in
// time, closes the connection. A dead reply is the only signal a SILENTLY
// dropped link (NAT/conntrack expiry, suspended laptop, cable pull) ever gives:
// without it, a request blocks in the ssh transport until the kernel TCP timeout
// (many minutes), which — with washmount's per-op timeout abandoning the parked
// goroutine — would leak a concurrency slot per hung op and eventually wedge the
// mount. SendRequest itself can block on a silent link (it waits for the reply
// through the same stuck transport), so it runs in its own goroutine bounded by
// a timeout.
// keepalive pings every interval; interval is also the per-ping reply deadline.
func keepalive(c *ssh.Client, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		errc := make(chan error, 1) // buffered: the ping goroutine never leaks on send
		go func() {
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			errc <- err
		}()
		select {
		case err := <-errc:
			if err != nil {
				c.Close()
				return
			}
		case <-time.After(interval):
			c.Close() // no reply in time → link is dead; unblock any parked op
			return
		}
	}
}

// parseTarget splits "[user@]host[:port]:/remote/path" into its parts.
func parseTarget(s string) (user, host, path string, err error) {
	at := strings.IndexByte(s, '@')
	if at >= 0 {
		user, s = s[:at], s[at+1:]
	} else {
		user = os.Getenv("USER")
	}
	// The remote path begins at the last colon that is followed by a slash, so
	// host:port:/path and host:/path both parse.
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || s[slash-1] != ':' {
		return "", "", "", fmt.Errorf("target must be [user@]host[:port]:/remote/path, got %q", s)
	}
	host, path = s[:slash-1], s[slash:]
	if !strings.Contains(host, ":") {
		host += ":22"
	}
	return user, host, path, nil
}

func dial(user, host string, insecure bool) (*ssh.Client, error) {
	authSock := os.Getenv("SSH_AUTH_SOCK")
	if authSock == "" {
		return nil, fmt.Errorf("no SSH agent: set up ssh-add (SSH_AUTH_SOCK is empty)")
	}
	conn, err := net.Dial("unix", authSock)
	if err != nil {
		return nil, fmt.Errorf("dial ssh agent: %w", err)
	}
	ag := agent.NewClient(conn)

	hostKeyCb := ssh.InsecureIgnoreHostKey()
	if !insecure {
		kh, err := knownhosts.New(os.ExpandEnv("$HOME/.ssh/known_hosts"))
		if err != nil {
			return nil, fmt.Errorf("known_hosts (use -insecure-host-key to bypass): %w", err)
		}
		hostKeyCb = kh
	}

	return ssh.Dial("tcp", host, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(ag.Signers)},
		HostKeyCallback: hostKeyCb,
		Timeout:         10 * time.Second,
	})
}
