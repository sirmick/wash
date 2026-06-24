// Package remotewatch carries filesystem-change events from a remote wash host
// (B) to the local desktop (A), so a FUSE mount whose data rides SFTP still gets
// live, push-based change notification — the half SFTP cannot provide.
//
// It is the "wash channel" of the two-channel design: SFTP moves bytes; this
// moves "it changed". B runs the real fswatch (inotify on B, where the files
// actually live and where all changes — SFTP-driven, B-local, or third-party —
// are observable); A turns the stream into a fswatch.Feed.
//
//	A: consumer ──Watch(localPath)──► Client ──{watch, remotePath}──► B: Server
//	                                    │                               │ src.Watch
//	A: consumer ◄──Feed events──── Client ◄──{event, remotePath}────── Server ◄ fswatch
//
// The Client implements fswatch.Source, so it is a drop-in for the local inotify
// Manager: a path under a remote mount routes here, everything else to inotify.
// Path translation (remote root ⇄ local mountpoint) happens at the Client edge,
// so the Feed and all consumers operate purely in local-path space.
package remotewatch

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/fswatch"
)

// msg is the line-delimited JSON wire frame. Paths are always in B's path space.
type msg struct {
	Op     string `json:"op"`               // "watch" | "unwatch" | "event"
	Path   string `json:"path"`             // remote (B-side) path
	Change string `json:"change,omitempty"` // event only: created|modified|deleted
}

func opFromChange(s string) (fswatch.Op, bool) {
	switch s {
	case "created":
		return fswatch.OpCreated, true
	case "modified":
		return fswatch.OpModified, true
	case "deleted":
		return fswatch.OpDeleted, true
	}
	return 0, false
}

// PathMap translates between the local mountpoint and the remote root. A mount
// of B's RemoteRoot at the local MountPoint makes these inverses.
type PathMap struct {
	MountPoint string // local, e.g. ~/wash/remote/host
	RemoteRoot string // remote, e.g. /home/user
}

// ToRemote maps a local path under MountPoint to its B-side path.
func (p PathMap) ToRemote(local string) string {
	rel, err := filepath.Rel(p.MountPoint, local)
	if err != nil {
		return local
	}
	return filepath.Join(p.RemoteRoot, rel)
}

// ToLocal maps a B-side path under RemoteRoot to its local path.
func (p PathMap) ToLocal(remote string) string {
	rel, err := filepath.Rel(p.RemoteRoot, remote)
	if err != nil {
		return remote
	}
	return filepath.Join(p.MountPoint, rel)
}

// ---- B side: Server ----

// Serve runs the B-side watch server until rw closes. It answers watch/unwatch
// requests by subscribing to src (B's local fswatch) and streams each change
// back as an event frame. Blocks; run it in a goroutine.
func Serve(rw io.ReadWriteCloser, src fswatch.Source) error {
	s := &server{rw: rw, src: src, enc: json.NewEncoder(rw), subs: map[string]fswatch.Subscription{}}
	return s.loop()
}

type server struct {
	rw    io.ReadWriteCloser
	src   fswatch.Source
	enc   *json.Encoder
	encMu sync.Mutex // serialize concurrent forward-goroutine writes

	mu   sync.Mutex
	subs map[string]fswatch.Subscription
}

func (s *server) loop() error {
	sc := bufio.NewScanner(s.rw)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var m msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			log.Printf("remotewatch: server bad frame: %v", err)
			continue
		}
		switch m.Op {
		case "watch":
			s.startWatch(m.Path)
		case "unwatch":
			s.stopWatch(m.Path)
		}
	}
	s.closeAll()
	return sc.Err()
}

func (s *server) startWatch(remotePath string) {
	s.mu.Lock()
	if _, ok := s.subs[remotePath]; ok {
		s.mu.Unlock()
		return
	}
	sub, err := s.src.Watch(remotePath)
	if err != nil {
		s.mu.Unlock()
		log.Printf("remotewatch: server watch %q: %v", remotePath, err)
		return
	}
	s.subs[remotePath] = sub
	s.mu.Unlock()

	go func() {
		for ev := range sub.Events() {
			s.send(msg{Op: "event", Path: ev.Path, Change: ev.Op.String()})
		}
	}()
}

func (s *server) stopWatch(remotePath string) {
	s.mu.Lock()
	sub := s.subs[remotePath]
	delete(s.subs, remotePath)
	s.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

func (s *server) send(m msg) {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	if err := s.enc.Encode(m); err != nil {
		log.Printf("remotewatch: server send: %v", err)
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	subs := s.subs
	s.subs = map[string]fswatch.Subscription{}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.Close()
	}
}

// ---- A side: Client ----

// errFixedTransport stops the connect loop for a NewClient (no reconnect): once
// its single transport dies there is nothing to re-dial.
var errFixedTransport = errors.New("remotewatch: fixed transport, no reconnect")

// Client is the A-side of the watch stream. It implements fswatch.Source, so a
// path under a remote mount can be watched through it exactly as a local path is
// watched through the inotify Manager. The Feed is stable across reconnects, so
// consumer Subscriptions (and the Hub's) survive a dropped channel: the connect
// loop re-dials and re-sends every active watch, transparently resubscribing.
type Client struct {
	dial func() (io.ReadWriteCloser, error)
	pm   PathMap
	feed *fswatch.Feed
	stop chan struct{}

	mu      sync.Mutex
	rw      io.ReadWriteCloser
	enc     *json.Encoder
	watched map[string]int // active remote paths (refcount) — re-sent on reconnect
	closed  bool
}

var _ fswatch.Source = (*Client)(nil)

// NewClient wraps a single fixed channel to B (no reconnect). Once it dies the
// client stops.
func NewClient(rw io.ReadWriteCloser, pm PathMap) *Client {
	used := false
	return newClient(func() (io.ReadWriteCloser, error) {
		if used {
			return nil, errFixedTransport
		}
		used = true
		return rw, nil
	}, pm)
}

// NewReconnectingClient re-dials the watch channel after a drop (dial re-opens
// `ssh <host> wash-fswatchd` in the service), re-subscribing the active watches
// so live updates self-heal across a transient ssh failure.
func NewReconnectingClient(dial func() (io.ReadWriteCloser, error), pm PathMap) *Client {
	return newClient(dial, pm)
}

func newClient(dial func() (io.ReadWriteCloser, error), pm PathMap) *Client {
	c := &Client{
		dial:    dial,
		pm:      pm,
		feed:    fswatch.NewFeed(),
		stop:    make(chan struct{}),
		watched: map[string]int{},
	}
	go c.run()
	return c
}

// Watch subscribes to localPath (under the mountpoint), asking B to watch the
// translated remote path. Closing the Subscription releases the remote watch.
func (c *Client) Watch(localPath string) (fswatch.Subscription, error) {
	sub, err := c.feed.Watch(localPath)
	if err != nil {
		return nil, err
	}
	remote := c.pm.ToRemote(localPath)
	c.mu.Lock()
	c.watched[remote]++
	if c.enc != nil { // send now if connected; else attach() re-sends on the next dial
		_ = c.enc.Encode(msg{Op: "watch", Path: remote})
	}
	c.mu.Unlock()
	return &clientSub{Subscription: sub, c: c, remote: remote}, nil
}

// Close stops the connect loop and the Feed.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	rw := c.rw
	c.rw, c.enc = nil, nil
	c.mu.Unlock()
	close(c.stop)
	if rw != nil {
		rw.Close() // unblock a parked serve()
	}
	return c.feed.Close()
}

// run dials, serves until the transport dies, and re-dials (with backoff) until
// Close. errFixedTransport ends it (a NewClient has no second transport).
func (c *Client) run() {
	// A connection that served at least this long counts as healthy; only then
	// does backoff reset. A dial that succeeds but dies sooner keeps escalating.
	const minHealthy = 5 * time.Second
	backoff := 100 * time.Millisecond
	grow := func() bool { // sleep `backoff`, then double it; false ⇒ stop requested
		select {
		case <-time.After(backoff):
		case <-c.stop:
			return false
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
		return true
	}
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		rw, err := c.dial()
		if err != nil {
			if errors.Is(err, errFixedTransport) {
				return
			}
			log.Printf("remotewatch: client dial: %v (retry in %s)", err, backoff)
			if !grow() {
				return
			}
			continue
		}
		// dial() only forks ssh — the connection isn't proven until serve() has
		// run a while. Resetting backoff here (pre-serve) let a dial-succeeds-
		// but-dies-instantly case (auth fail, host-key change, missing
		// wash-fswatchd) re-dial with zero delay: a tight ssh fork bomb on both
		// hosts (TODO-sftp-mount-bugs.md High). So time the serve and reset only
		// once it proved healthy; an instant death escalates like a dial error.
		start := time.Now()
		c.attach(rw)
		c.serve(rw) // blocks until the transport dies or Close
		c.detach(rw)
		if time.Since(start) >= minHealthy {
			backoff = 100 * time.Millisecond
		} else if !grow() {
			return
		}
	}
}

// attach makes rw current and re-subscribes every active watch, so a reconnect
// transparently restores the consumer's subscriptions on B.
func (c *Client) attach(rw io.ReadWriteCloser) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rw = rw
	c.enc = json.NewEncoder(rw)
	for remote := range c.watched {
		_ = c.enc.Encode(msg{Op: "watch", Path: remote})
	}
}

func (c *Client) detach(rw io.ReadWriteCloser) {
	c.mu.Lock()
	if c.rw == rw {
		c.rw, c.enc = nil, nil
	}
	c.mu.Unlock()
	rw.Close()
}

// serve reads events off rw into the Feed until rw dies.
func (c *Client) serve(rw io.ReadWriteCloser) {
	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var m msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			log.Printf("remotewatch: client bad frame: %v", err)
			continue
		}
		if m.Op != "event" {
			continue
		}
		op, ok := opFromChange(m.Change)
		if !ok {
			continue
		}
		// Translate B's path into local space before it enters the Feed.
		c.feed.Emit(fswatch.Event{Op: op, Path: c.pm.ToLocal(m.Path)})
	}
}

// clientSub wraps a Feed subscription so closing it also releases the remote
// watch on B.
type clientSub struct {
	fswatch.Subscription
	c      *Client
	remote string
}

func (s *clientSub) Close() {
	c := s.c
	c.mu.Lock()
	if c.watched[s.remote] > 0 {
		c.watched[s.remote]--
	}
	if c.watched[s.remote] == 0 {
		delete(c.watched, s.remote)
		if c.enc != nil {
			_ = c.enc.Encode(msg{Op: "unwatch", Path: s.remote})
		}
	}
	c.mu.Unlock()
	s.Subscription.Close()
}
