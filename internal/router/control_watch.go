package router

import (
	"context"
	"encoding/json"
	"net"
	"sync"
)

// The control socket's `watch` verb: every app_msg an instance's backend
// sends its frontend, streamed to the caller as JSON lines, so a client
// with no browser (a test harness, a script) sees what the frontend would.
// With `launch` and `msg` that is enough to drive an app headless.
//
//	→ {"t":"watch","instance_id":"…"}
//	← {"t":"watching"}
//	← {"t":"msg","data":{…}}            one per backend → frontend message
//	← {"t":"gone"}                      the instance ended
//	← {"t":"overflow"}                  the caller fell behind; the watch ends
//
// The watch ends when a write to the caller fails: it has gone. A caller
// that only half-closes (sent its request, reads on) keeps watching.

// watchBuffer is how many messages a watch may lag behind before it is
// ended with overflow, rather than losing messages silently.
const watchBuffer = 4096

type appMsgStream struct {
	ch   chan json.RawMessage
	once sync.Once
	// ended is closed when the instance goes (gone) or the stream falls
	// behind (overflowed set first).
	ended      chan struct{}
	overflowed bool
}

func (s *appMsgStream) end() { s.once.Do(func() { close(s.ended) }) }

// WatchAppMsgs streams every app_msg the instance sends its frontend.
func (r *Router) WatchAppMsgs(instanceID string) (*appMsgStream, func()) {
	s := &appMsgStream{ch: make(chan json.RawMessage, watchBuffer), ended: make(chan struct{})}
	r.appMsgMu.Lock()
	if r.appMsgStreams[instanceID] == nil {
		r.appMsgStreams[instanceID] = map[*appMsgStream]struct{}{}
	}
	r.appMsgStreams[instanceID][s] = struct{}{}
	r.appMsgMu.Unlock()
	return s, func() {
		r.appMsgMu.Lock()
		delete(r.appMsgStreams[instanceID], s)
		if len(r.appMsgStreams[instanceID]) == 0 {
			delete(r.appMsgStreams, instanceID)
		}
		r.appMsgMu.Unlock()
	}
}

// feedAppMsgStreams hands one outbound message to the instance's watches.
func (r *Router) feedAppMsgStreams(instanceID string, data []byte) {
	r.appMsgMu.Lock()
	defer r.appMsgMu.Unlock()
	for s := range r.appMsgStreams[instanceID] {
		select {
		case s.ch <- append(json.RawMessage(nil), data...):
		default:
			s.overflowed = true
			s.end()
			delete(r.appMsgStreams[instanceID], s)
		}
	}
}

// endAppMsgStreams tells the instance's watches it is gone.
func (r *Router) endAppMsgStreams(instanceID string) {
	r.appMsgMu.Lock()
	defer r.appMsgMu.Unlock()
	for s := range r.appMsgStreams[instanceID] {
		s.end()
	}
	delete(r.appMsgStreams, instanceID)
}

func (r *Router) controlWatch(ctx context.Context, conn net.Conn, instanceID string) {
	if instanceID == "" {
		writeControlResponse(conn, map[string]any{"t": "error", "code": "bad_request", "msg": "missing instance_id"})
		return
	}
	if r.appByInstance(instanceID) == nil {
		writeControlResponse(conn, map[string]any{"t": "error", "code": "not_found", "msg": instanceID})
		return
	}
	s, cancel := r.WatchAppMsgs(instanceID)
	defer cancel()
	send := func(m map[string]any) bool {
		b, err := json.Marshal(m)
		if err != nil {
			return true
		}
		_, err = conn.Write(append(b, '\n'))
		return err == nil
	}
	if !send(map[string]any{"t": "watching"}) {
		return
	}
	for {
		select {
		case data := <-s.ch:
			if !send(map[string]any{"t": "msg", "data": data}) {
				return
			}
		case <-s.ended:
			// Drain what arrived before the end, then say why it ended.
		drain:
			for {
				select {
				case data := <-s.ch:
					if !send(map[string]any{"t": "msg", "data": data}) {
						return
					}
				default:
					break drain
				}
			}
			r.appMsgMu.Lock()
			overflowed := s.overflowed
			r.appMsgMu.Unlock()
			if overflowed {
				send(map[string]any{"t": "overflow"})
			} else {
				send(map[string]any{"t": "gone"})
			}
			return
		case <-ctx.Done():
			return
		}
	}
}
