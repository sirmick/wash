package fswatch

import "sync"

// Hub is the engine of a shared, cross-process watch service. Today each wash
// app runs its own inotify Manager; the Hub centralizes that so one process
// watches on behalf of many subscribers — which both lets remote-mount paths be
// served (via a Router src) and collapses N inotify instances into one.
//
// Subscribers are opaque ids (in the service, a remote app instance). The Hub
// keeps a single underlying Subscription per path through src (a Router fronting
// local inotify and remote mounts) and fans that path's events to exactly the
// subscribers that asked for it. The app layer supplies emit, which delivers one
// event to one subscriber (over the bus).
type Hub struct {
	src  Source
	emit func(subscriberID string, ev Event)

	mu    sync.Mutex
	paths map[string]*hubPath
}

type hubPath struct {
	sub         Subscription
	subscribers map[string]struct{}
	done        chan struct{}
}

// NewHub returns a Hub watching through src and delivering via emit. emit is
// called without the Hub lock held, so it may call back into the Hub.
func NewHub(src Source, emit func(subscriberID string, ev Event)) *Hub {
	return &Hub{src: src, emit: emit, paths: make(map[string]*hubPath)}
}

// Subscribe registers subscriberID's interest in path, starting the underlying
// watch on first interest. Idempotent per (subscriberID, path).
func (h *Hub) Subscribe(subscriberID, path string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	hp := h.paths[path]
	if hp == nil {
		sub, err := h.src.Watch(path)
		if err != nil {
			return err
		}
		hp = &hubPath{sub: sub, subscribers: make(map[string]struct{}), done: make(chan struct{})}
		h.paths[path] = hp
		go h.forward(hp)
	}
	hp.subscribers[subscriberID] = struct{}{}
	return nil
}

// forward fans a path's events to its current subscribers until the path is torn
// down. The subscriber set is snapshotted under lock, then emit runs unlocked.
func (h *Hub) forward(hp *hubPath) {
	for {
		select {
		case ev, ok := <-hp.sub.Events():
			if !ok {
				return
			}
			h.mu.Lock()
			ids := make([]string, 0, len(hp.subscribers))
			for id := range hp.subscribers {
				ids = append(ids, id)
			}
			h.mu.Unlock()
			for _, id := range ids {
				h.emit(id, ev)
			}
		case <-hp.done:
			return
		}
	}
}

// Unsubscribe drops subscriberID's interest in path, tearing the watch down when
// the last subscriber leaves.
func (h *Hub) Unsubscribe(subscriberID, path string) {
	h.mu.Lock()
	hp := h.paths[path]
	if hp == nil {
		h.mu.Unlock()
		return
	}
	delete(hp.subscribers, subscriberID)
	last := len(hp.subscribers) == 0
	if last {
		delete(h.paths, path)
	}
	h.mu.Unlock()
	if last {
		close(hp.done)
		hp.sub.Close()
	}
}

// RemoveSubscriber drops subscriberID from every path — call when a subscriber
// disconnects so its watches don't leak.
func (h *Hub) RemoveSubscriber(subscriberID string) {
	h.mu.Lock()
	var dead []*hubPath
	for path, hp := range h.paths {
		if _, ok := hp.subscribers[subscriberID]; !ok {
			continue
		}
		delete(hp.subscribers, subscriberID)
		if len(hp.subscribers) == 0 {
			dead = append(dead, hp)
			delete(h.paths, path)
		}
	}
	h.mu.Unlock()
	for _, hp := range dead {
		close(hp.done)
		hp.sub.Close()
	}
}

// Close tears down every watch.
func (h *Hub) Close() error {
	h.mu.Lock()
	paths := h.paths
	h.paths = make(map[string]*hubPath)
	h.mu.Unlock()
	for _, hp := range paths {
		close(hp.done)
		hp.sub.Close()
	}
	return nil
}
