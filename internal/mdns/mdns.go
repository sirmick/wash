// Package mdns is a small, self-contained multicast-DNS (RFC 6762) /
// DNS-SD (RFC 6763) browser + responder, scoped to exactly what
// wash-discovery needs: announce this box under one service type and
// browse the local link for peers announcing the same.
//
// It is deliberately NOT a general zeroconf stack — no third-party
// dependency, no probing/conflict-resolution, no reverse lookups. It
// speaks just enough of the wire protocol (PTR/SRV/TXT/A in one
// self-contained response packet) to make wash boxes find each other on
// a flat subnet, the way Avahi/Bonjour do. Cross-subnet discovery is a
// non-goal here by design — mDNS is link-local (TTL=1) and can't cross a
// router; that case is handled by other discovery providers (unicast
// CIDR probe, ssh-config import) layered above this in the supervisor.
//
// Coexistence: the socket is opened with SO_REUSEADDR + SO_REUSEPORT so
// it shares :5353 with a system Avahi/mDNSResponder rather than failing
// to bind. Both responders answering is fine — mDNS is designed for it.
package mdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

const (
	// Service is the DNS-SD service type wash advertises and browses.
	Service = "_wash._tcp"
	// domain is the mDNS link-local domain. Combined with Service this
	// gives the fully-qualified browse name "_wash._tcp.local.".
	domain = "local"

	mcastIP   = "224.0.0.251"
	mcastPort = 5353

	// recordTTL is the advertised lifetime of our records (seconds). 120
	// is the conventional mDNS default; a browser re-queries well within
	// it, so a vanished host ages out of the caller's view promptly.
	recordTTL = 120
	// cacheFlush is the mDNS "unique record" bit OR'd into a record's
	// class so receivers replace any cached copy (RFC 6762 §10.2). Set on
	// our SRV/TXT/A (we own them); left off the shared PTR.
	cacheFlush = 0x8000
)

// fqService is "_wash._tcp.local." — the PTR query/answer name.
func fqService() string { return Service + "." + domain + "." }

// Service describes what this box announces. The zero value advertises
// nothing; pass a populated *Service to New via Options.Advertise.
type ServiceInfo struct {
	// Instance is the human-facing instance label (typically the
	// hostname). It becomes "<Instance>._wash._tcp.local.".
	Instance string
	// Port is the SSH port to advertise in the SRV record — the port a
	// peer connects to, NOT the mDNS port. Callers SSH here to bring the
	// peer's apps up.
	Port int
	// Text are TXT key=value strings (e.g. "wash=1", "v=0.9.0").
	Text []string
	// IPv4 are the A-record addresses peers should reach us on. Usually
	// this box's routable interface IPs.
	IPv4 []net.IP
}

// Entry is one discovered peer, assembled from a response's PTR+SRV+TXT+A.
type Entry struct {
	Instance string   // instance label (the part before "._wash._tcp")
	Host     string   // SRV target hostname, ".local." trimmed
	Addrs    []net.IP // A-record addresses
	Port     int      // SRV port (the peer's SSH port)
	Text     []string // TXT strings
}

// Options configures a Server.
type Options struct {
	// Advertise, if non-nil, makes the server respond to "_wash._tcp"
	// queries with this box's records and emit gratuitous announcements.
	Advertise *ServiceInfo
	// OnEntry, if non-nil, makes the server browse: it sends periodic PTR
	// queries and calls OnEntry for every peer parsed from a response.
	// Called from the read goroutine — keep it non-blocking.
	OnEntry func(Entry)
	// QueryInterval is the browse re-query cadence. Zero defaults to 60s.
	QueryInterval time.Duration
}

// Server is one open mDNS socket running advertise and/or browse.
type Server struct {
	pc        *ipv4.PacketConn
	ifaces    []net.Interface // multicast-capable interfaces we joined
	localIPs  map[string]bool // our own addrs, for self-packet filtering
	dst       *net.UDPAddr
	opts      Options
	advertise *ServiceInfo

	sendMu sync.Mutex // SetMulticastInterface mutates shared conn state

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New opens the multicast socket, joins the mDNS group on every
// multicast-capable interface, and starts the read loop plus (per Options)
// the announce/browse goroutines. Close stops everything.
func New(opts Options) (*Server, error) {
	if opts.QueryInterval == 0 {
		opts.QueryInterval = 60 * time.Second
	}
	pc, ifaces, err := openMulticast()
	if err != nil {
		return nil, err
	}
	s := &Server{
		pc:        pc,
		ifaces:    ifaces,
		localIPs:  localAddrSet(),
		dst:       &net.UDPAddr{IP: net.ParseIP(mcastIP), Port: mcastPort},
		opts:      opts,
		advertise: opts.Advertise,
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	s.wg.Add(1)
	go s.readLoop(ctx)

	if s.advertise != nil {
		// A couple of gratuitous announcements so peers already browsing
		// learn about us without waiting for their next query.
		s.announce()
		s.wg.Add(1)
		go s.announceLoop(ctx)
	}
	if s.opts.OnEntry != nil {
		s.query() // kick an immediate browse
		s.wg.Add(1)
		go s.queryLoop(ctx)
	}
	return s, nil
}

// Close stops the goroutines and closes the socket.
func (s *Server) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	// Unblock readLoop's ReadFrom first: on some platforms closing the
	// PacketConn does not reliably interrupt a read already parked in the
	// kernel, so wg.Wait() would hang. A past read deadline forces ReadFrom
	// to return immediately; readLoop returns on any error.
	_ = s.pc.SetReadDeadline(time.Now())
	err := s.pc.Close()
	s.wg.Wait()
	return err
}

// openMulticast binds :5353 (SO_REUSEADDR+SO_REUSEPORT so we coexist with
// a system responder) and joins 224.0.0.251 on each up, multicast-capable,
// non-loopback interface. It is not fatal if some interfaces refuse the
// join — as long as at least one succeeds.
func openMulticast() (*ipv4.PacketConn, []net.Interface, error) {
	lc := net.ListenConfig{Control: reusePort}
	conn, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", mcastPort))
	if err != nil {
		return nil, nil, fmt.Errorf("mdns: bind :%d: %w", mcastPort, err)
	}
	pc := ipv4.NewPacketConn(conn)
	// We need the receive interface to filter our own packets reliably.
	_ = pc.SetControlMessage(ipv4.FlagInterface, true)
	pc.SetMulticastLoopback(true)

	group := &net.UDPAddr{IP: net.ParseIP(mcastIP)}
	all, err := net.Interfaces()
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("mdns: interfaces: %w", err)
	}
	var joined []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if err := pc.JoinGroup(&ifi, group); err != nil {
			continue // interface may not route multicast; skip it
		}
		joined = append(joined, ifi)
	}
	if len(joined) == 0 {
		conn.Close()
		return nil, nil, errors.New("mdns: no multicast-capable interface")
	}
	return pc, joined, nil
}

// readLoop reads packets, answers queries (if advertising) and parses
// responses into Entries (if browsing). Self-originated packets are
// dropped by source-IP so we neither answer our own queries nor discover
// ourselves.
func (s *Server) readLoop(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := s.pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			return // socket closed
		}
		if ua, ok := src.(*net.UDPAddr); ok && s.localIPs[ua.IP.String()] {
			continue // our own packet looped back
		}
		_ = cm
		var msg dnsmessage.Message
		if err := msg.Unpack(buf[:n]); err != nil {
			continue
		}
		if msg.Header.Response {
			if s.opts.OnEntry != nil {
				s.handleResponse(&msg)
			}
		} else if s.advertise != nil {
			s.handleQuery(&msg)
		}
	}
}

// handleQuery answers a query that asks for our service (PTR on
// "_wash._tcp.local.") with a single self-contained response packet.
func (s *Server) handleQuery(msg *dnsmessage.Message) {
	want := strings.ToLower(fqService())
	for _, q := range msg.Questions {
		if q.Type != dnsmessage.TypePTR && q.Type != dnsmessage.TypeALL {
			continue
		}
		if strings.ToLower(q.Name.String()) == want {
			s.announce()
			return
		}
	}
}

// handleResponse assembles peers from a response and emits each via
// OnEntry. SRV/TXT/A may arrive in Answers or Additionals, so both are
// scanned and correlated by name.
func (s *Server) handleResponse(msg *dnsmessage.Message) {
	type rec struct {
		host string
		port int
		text []string
	}
	byInstance := map[string]*rec{}      // instance fqdn -> SRV/TXT
	addrsByHost := map[string][]net.IP{} // host fqdn -> A addresses
	var ptrTargets []string              // instance fqdns the PTR points at

	scan := func(rs []dnsmessage.Resource) {
		for _, r := range rs {
			name := strings.ToLower(r.Header.Name.String())
			switch b := r.Body.(type) {
			case *dnsmessage.PTRResource:
				if name == strings.ToLower(fqService()) {
					ptrTargets = append(ptrTargets, strings.ToLower(b.PTR.String()))
				}
			case *dnsmessage.SRVResource:
				e := byInstance[name]
				if e == nil {
					e = &rec{}
					byInstance[name] = e
				}
				e.host = strings.ToLower(b.Target.String())
				e.port = int(b.Port)
			case *dnsmessage.TXTResource:
				e := byInstance[name]
				if e == nil {
					e = &rec{}
					byInstance[name] = e
				}
				e.text = append(e.text, b.TXT...)
			case *dnsmessage.AResource:
				ip := net.IP(b.A[:])
				addrsByHost[name] = append(addrsByHost[name], ip)
			}
		}
	}
	scan(msg.Answers)
	scan(msg.Additionals)

	// Emit one Entry per instance we have an SRV for (the connectable
	// fact). PTR-only instances with no SRV are skipped — nothing to dial.
	seen := map[string]bool{}
	emit := func(inst string) {
		if seen[inst] {
			return
		}
		seen[inst] = true
		// Only emit instances under OUR service. The fallback loop below
		// scans every SRV in the packet, and a busy LAN multicasts records
		// for unrelated services (printers, Spotify, Chromecast); without
		// this guard they'd leak in as bogus candidates.
		if !strings.HasSuffix(inst, "."+strings.ToLower(fqService())) {
			return
		}
		e := byInstance[inst]
		if e == nil || e.host == "" {
			return
		}
		addrs := append([]net.IP(nil), addrsByHost[e.host]...)
		s.opts.OnEntry(Entry{
			Instance: trimService(inst),
			Host:     trimDot(e.host),
			Addrs:    addrs,
			Port:     e.port,
			Text:     e.text,
		})
	}
	for _, t := range ptrTargets {
		emit(t)
	}
	// Some responders omit the PTR on a directed answer; cover instances
	// that arrived via SRV/TXT alone.
	for inst := range byInstance {
		emit(inst)
	}
}

// announce sends our self-contained PTR+SRV+TXT+A response on every joined
// interface.
func (s *Server) announce() {
	if s.advertise == nil {
		return
	}
	pkt, err := s.buildAnnouncement()
	if err != nil {
		return
	}
	s.send(pkt)
}

// query sends a PTR question for the wash service on every joined interface.
func (s *Server) query() {
	q := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: false},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(fqService()),
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
		}},
	}
	pkt, err := q.Pack()
	if err != nil {
		return
	}
	s.send(pkt)
}

func (s *Server) announceLoop(ctx context.Context) {
	defer s.wg.Done()
	// Two quick re-announcements after start (RFC 6762 suggests a small
	// burst), then quiet — we rely on answering queries thereafter.
	t := time.NewTimer(time.Second)
	defer t.Stop()
	bursts := 2
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.announce()
			bursts--
			if bursts <= 0 {
				return
			}
			t.Reset(time.Second)
		}
	}
}

func (s *Server) queryLoop(ctx context.Context) {
	defer s.wg.Done()
	t := time.NewTicker(s.opts.QueryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.query()
		}
	}
}

// send writes pkt to the multicast group out of each joined interface.
// SetMulticastInterface mutates conn state, so it's serialized.
func (s *Server) send(pkt []byte) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	for i := range s.ifaces {
		if err := s.pc.SetMulticastInterface(&s.ifaces[i]); err != nil {
			continue
		}
		_, _ = s.pc.WriteTo(pkt, nil, s.dst)
	}
}

// buildAnnouncement packs the response advertising this box: PTR (shared)
// + SRV/TXT/A (cache-flush, since we own them).
func (s *Server) buildAnnouncement() ([]byte, error) {
	a := s.advertise
	instName := a.Instance + "." + Service + "." + domain + "."
	hostName := a.Instance + "." + domain + "."

	inet := dnsmessage.ClassINET
	flush := dnsmessage.Class(cacheFlush | uint16(dnsmessage.ClassINET))

	answers := []dnsmessage.Resource{
		{
			Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(fqService()), Type: dnsmessage.TypePTR, Class: inet, TTL: recordTTL},
			Body:   &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(instName)},
		},
		{
			Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(instName), Type: dnsmessage.TypeSRV, Class: flush, TTL: recordTTL},
			Body:   &dnsmessage.SRVResource{Port: uint16(a.Port), Target: dnsmessage.MustNewName(hostName)},
		},
		{
			Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(instName), Type: dnsmessage.TypeTXT, Class: flush, TTL: recordTTL},
			Body:   &dnsmessage.TXTResource{TXT: txtOrEmpty(a.Text)},
		},
	}
	for _, ip := range a.IPv4 {
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		var arr [4]byte
		copy(arr[:], v4)
		answers = append(answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(hostName), Type: dnsmessage.TypeA, Class: flush, TTL: recordTTL},
			Body:   &dnsmessage.AResource{A: arr},
		})
	}
	msg := dnsmessage.Message{
		Header:  dnsmessage.Header{Response: true, Authoritative: true},
		Answers: answers,
	}
	return msg.Pack()
}

// txtOrEmpty ensures a TXT record has at least one (empty) string — a TXT
// with zero strings is invalid on the wire.
func txtOrEmpty(t []string) []string {
	if len(t) == 0 {
		return []string{""}
	}
	return t
}

// trimDot removes the trailing "." from a fqdn.
func trimDot(s string) string { return strings.TrimSuffix(s, ".") }

// trimService turns "host._wash._tcp.local." into "host".
func trimService(inst string) string {
	suffix := "." + strings.ToLower(fqService())
	inst = strings.ToLower(inst)
	if i := strings.Index(inst, suffix); i >= 0 {
		return inst[:i]
	}
	return trimDot(inst)
}

// localAddrSet returns the set of this box's interface IP strings, used to
// drop our own looped-back packets.
func localAddrSet() map[string]bool {
	set := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return set
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			set[ipn.IP.String()] = true
		}
	}
	return set
}
