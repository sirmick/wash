package remote

// Host discovery — surfacing connect *candidates* the user hasn't saved
// or connected to yet (docs/REMOTE.md §6.1). The first provider is mDNS:
// this box announces itself under "_wash._tcp.local." and browses the
// local link for peers doing the same, so two wash machines on a flat
// subnet find each other with zero config.
//
// Discovery is built as a small provider seam so later, subnet-crossing
// sources (a unicast CIDR probe, ssh-config/known_hosts import) can be
// added without touching the FE — every provider emits the same Candidate
// and the discoverer aggregates them into State.Candidates, tagged by
// source. mDNS alone is link-local (TTL=1) and cannot cross a router, so
// it deliberately does NOT span subnets/VLANs; that is the job of the
// providers still to come.
//
// Lifecycle: the discoverer runs for the supervisor's (router) lifetime —
// advertising wants to persist so peers can find us whenever, and browsing
// is cheap (passive listen + a slow re-query). It publishes into the same
// StateService the connect FE already subscribes to.

import (
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/mdns"
)

// Candidate is one discovered host — a connect target not yet saved or
// connected. Source tags which provider found it ("mdns" today). Addr is
// the IP to SSH to (more reliable than an mDNS ".local" name); Host is the
// friendly display name.
type Candidate struct {
	Host   string `json:"host"`
	Addr   string `json:"addr"`
	Port   int    `json:"port,omitempty"`
	Source string `json:"source"`
	Wash   bool   `json:"wash,omitempty"`
}

const (
	// candidateTTL is how long a candidate survives without being re-seen
	// before the discoverer prunes it. Comfortably longer than the mDNS
	// re-query cadence so a still-present peer never flickers out.
	candidateTTL = 5 * time.Minute
	// pruneInterval is how often stale candidates are swept.
	pruneInterval = time.Minute
	// defaultSSHPort is the SSH port advertised/assumed when none is known.
	defaultSSHPort = 22
)

// stateMutator is the slice of StateService the discoverer needs — kept as
// an interface so tests can drive it without a live Bus.
type stateMutator interface {
	Mutate(func(*State))
}

// discoverer aggregates candidates from one or more providers and mirrors
// them into the published State, pruning ones that age out.
type discoverer struct {
	svc stateMutator

	mu    sync.Mutex
	cands map[string]seenCandidate // key: source + "|" + addr

	mdnsSrv *mdns.Server
}

type seenCandidate struct {
	c    Candidate
	seen time.Time
	// sticky candidates never age out (a manually-pinned static host has no
	// re-announcement to refresh its seen time, unlike an mDNS peer).
	sticky bool
}

func newDiscoverer(svc stateMutator) *discoverer {
	return &discoverer{svc: svc, cands: map[string]seenCandidate{}}
}

// start brings up the mDNS provider (advertise + browse) and the prune
// loop. Failure to open the mDNS socket is logged, not fatal — discovery
// is best-effort and the rest of wash-remote works without it.
func (d *discoverer) start() {
	adv := buildAdvertisement()
	srv, err := mdns.New(mdns.Options{
		Advertise: adv,
		OnEntry:   d.onMDNS,
		Logf:      log.Printf,
		Debug:     os.Getenv("WASH_MDNS_DEBUG") != "",
	})
	if err != nil {
		log.Printf("wash-remote: discovery mdns: %v (continuing without it)", err)
	} else {
		d.mdnsSrv = srv
		if adv != nil {
			log.Printf("wash-remote: discovery advertising host=%q ips=%v on _wash._tcp.local", adv.Instance, adv.IPv4)
		} else {
			// A box with no advertisement can still browse, but no peer can
			// find IT. Usual cause: no routable IPv4 (container/link-local
			// only) or the WASH_DISCOVERY_NO_ADVERTISE opt-out. Logged loudly
			// because it's a silent half-failure otherwise.
			log.Printf("wash-remote: discovery NOT advertising — no routable IPv4 or opted out; this box is invisible to peers")
		}
	}
	d.startStatic()
	go d.pruneLoop()
}

// startStatic seeds candidates from WASH_DISCOVERY_STATIC — a comma-separated
// list of manually-pinned hosts (`name=addr[:port]` or bare `addr[:port]`).
// It's the first cross-subnet provider behind the seam (mDNS can't leave the
// link) and the deterministic hook the e2e drives, since real multicast needs
// two hosts. Entries are sticky: with no announcement to refresh them they'd
// otherwise age out of the candidate list.
func (d *discoverer) startStatic() {
	raw := os.Getenv("WASH_DISCOVERY_STATIC")
	if raw == "" {
		return
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, target := "", entry
		if i := strings.Index(entry, "="); i >= 0 {
			name, target = strings.TrimSpace(entry[:i]), strings.TrimSpace(entry[i+1:])
		}
		addr, port := target, 0
		if h, p, err := net.SplitHostPort(target); err == nil {
			addr = h
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
		if addr == "" {
			continue
		}
		if name == "" {
			name = addr
		}
		if port == defaultSSHPort {
			port = 0
		}
		d.observe(Candidate{Host: name, Addr: addr, Port: port, Source: "static"}, true)
	}
}

// onMDNS turns a browsed peer into a Candidate. Called from the mDNS read
// goroutine.
func (d *discoverer) onMDNS(e mdns.Entry) {
	addr := firstIPv4(e.Addrs)
	if addr == "" {
		return // nothing connectable without an address
	}
	host := e.Host
	if host == "" {
		host = e.Instance
	}
	port := e.Port
	if port == defaultSSHPort {
		port = 0 // omit the default from the wire
	}
	d.observe(Candidate{
		Host:   strings.TrimSuffix(host, ".local"),
		Addr:   addr,
		Port:   port,
		Source: "mdns",
		Wash:   true, // it answered on the wash service
	}, false)
}

// observe records (or refreshes) a candidate and republishes if it is new or
// any of its fields changed (a peer re-announcing identical data must not
// churn the FE). sticky marks a candidate that should never be pruned.
func (d *discoverer) observe(c Candidate, sticky bool) {
	key := c.Source + "|" + c.Addr
	d.mu.Lock()
	prev, existed := d.cands[key]
	d.cands[key] = seenCandidate{c: c, seen: time.Now(), sticky: sticky}
	d.mu.Unlock()
	if !existed {
		log.Printf("wash-remote: discovered host=%q addr=%s port=%d source=%s wash=%v", c.Host, c.Addr, c.Port, c.Source, c.Wash)
	}
	if !existed || prev.c != c {
		d.publish()
	}
}

// pruneLoop drops candidates not seen within candidateTTL.
func (d *discoverer) pruneLoop() {
	t := time.NewTicker(pruneInterval)
	defer t.Stop()
	for range t.C {
		if d.pruneBefore(time.Now().Add(-candidateTTL)) {
			d.publish()
		}
	}
}

// pruneBefore removes candidates last seen before cutoff, reporting
// whether anything was dropped.
func (d *discoverer) pruneBefore(cutoff time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := false
	for k, sc := range d.cands {
		if !sc.sticky && sc.seen.Before(cutoff) {
			delete(d.cands, k)
			changed = true
		}
	}
	return changed
}

// publish snapshots the current candidate set into State, sorted for a
// stable FE order.
func (d *discoverer) publish() {
	d.mu.Lock()
	out := make([]Candidate, 0, len(d.cands))
	for _, sc := range d.cands {
		out = append(out, sc.c)
	}
	d.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Addr < out[j].Addr
	})
	d.svc.Mutate(func(s *State) { s.Candidates = out })
}

// buildAdvertisement describes this box for mDNS, or returns nil if
// advertising is disabled (WASH_DISCOVERY_NO_ADVERTISE) or we have no
// routable address to announce. A settings-UI toggle is a later follow-up;
// the env var is the escape hatch until then.
func buildAdvertisement() *mdns.ServiceInfo {
	if os.Getenv("WASH_DISCOVERY_NO_ADVERTISE") != "" {
		return nil
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return nil
	}
	host = strings.TrimSuffix(host, ".local")
	ips := routableIPv4()
	if len(ips) == 0 {
		return nil
	}
	return &mdns.ServiceInfo{
		Instance: host,
		Port:     defaultSSHPort,
		Text:     []string{"wash=1"},
		IPv4:     ips,
	}
}

// routableIPv4 returns this box's non-loopback, non-link-local IPv4
// addresses on real interfaces — the ones a peer could SSH to. Mirrors the
// "routable v4" filter AND the docker-plumbing exclusion in
// apps/session/be/netifaces.go (kept local: single consumer). Skipping
// docker bridges matters: their 172.17.0.1/172.18.0.1 addresses are
// unreachable from a peer and every docker host shares them, so advertising
// them would hand out bogus connect targets.
func routableIPv4() []net.IP {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, ifc := range all {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || dockerIface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 == nil || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, v4)
		}
	}
	return out
}

// dockerIface matches container plumbing — the default bridge, veth pairs,
// and docker's per-network bridges (br-<12 hex>). Mirrors the same-named
// helper in apps/session/be/netifaces.go; wash's own bridges (br0/br1) and
// vlans (eth0.10) are intentionally NOT matched.
func dockerIface(name string) bool {
	switch {
	case name == "docker0", strings.HasPrefix(name, "veth"):
		return true
	case strings.HasPrefix(name, "br-") && len(name) == 15: // "br-" + 12 hex
		return true
	default:
		return false
	}
}

// firstIPv4 returns the first IPv4 address as a string, or "".
func firstIPv4(addrs []net.IP) string {
	for _, ip := range addrs {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}
