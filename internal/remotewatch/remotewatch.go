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
	"io"
	"log"
	"path/filepath"
	"sync"

	"github.com/sirmick/wash/internal/fswatch"
)

// msg is the line-delimited JSON wire frame. Paths are always in B's path space.
type msg struct {
	Op     string `json:"op"`               // "watch" | "unwatch" | "event"
	Path   string `json:"path"`             // remote (B-side) path
	Change string `json:"change,omitempty"` // event only: created|modified|deleted
}

func changeString(op fswatch.Op) string { return op.String() }

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
	rw   io.ReadWriteCloser
	src  fswatch.Source
	enc  *json.Encoder
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
			s.send(msg{Op: "event", Path: ev.Path, Change: changeString(ev.Op)})
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

// Client is the A-side of the watch stream. It implements fswatch.Source, so a
// path under a remote mount can be watched through it exactly as a local path
// is watched through the inotify Manager.
type Client struct {
	rw   io.ReadWriteCloser
	feed *fswatch.Feed
	pm   PathMap

	encMu sync.Mutex
	enc   *json.Encoder
}

var _ fswatch.Source = (*Client)(nil)

// NewClient wraps the channel to B and starts reading events into a Feed.
func NewClient(rw io.ReadWriteCloser, pm PathMap) *Client {
	c := &Client{rw: rw, feed: fswatch.NewFeed(), pm: pm, enc: json.NewEncoder(rw)}
	go c.readLoop()
	return c
}

// Watch subscribes to localPath (under the mountpoint), asking B to watch the
// translated remote path. The returned Subscription's Close also tells B to
// unwatch, so the remote watch is released when the last local consumer goes.
func (c *Client) Watch(localPath string) (fswatch.Subscription, error) {
	sub, err := c.feed.Watch(localPath)
	if err != nil {
		return nil, err
	}
	remote := c.pm.ToRemote(localPath)
	c.send(msg{Op: "watch", Path: remote})
	return &clientSub{Subscription: sub, c: c, remote: remote}, nil
}

// Close stops the client and its Feed. The channel to B is the caller's.
func (c *Client) Close() error {
	return c.feed.Close()
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.rw)
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
		// Translate B's path into local space before it enters the Feed, so
		// every consumer sees only local paths.
		c.feed.Emit(fswatch.Event{Op: op, Path: c.pm.ToLocal(m.Path)})
	}
}

func (c *Client) send(m msg) {
	c.encMu.Lock()
	defer c.encMu.Unlock()
	if err := c.enc.Encode(m); err != nil {
		log.Printf("remotewatch: client send: %v", err)
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
	s.c.send(msg{Op: "unwatch", Path: s.remote})
	s.Subscription.Close()
}
