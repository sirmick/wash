package mdns

import (
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// TestAnnouncementRoundTrip packs an advertisement the way a peer would,
// then parses it back through the browse path — the two halves that must
// agree on the wire. It bypasses the socket (no real multicast in CI):
// buildAnnouncement → Unpack → handleResponse.
func TestAnnouncementRoundTrip(t *testing.T) {
	adv := &ServiceInfo{
		Instance: "peerbox",
		Port:     22,
		Text:     []string{"wash=1", "v=0.9.0"},
		IPv4:     []net.IP{net.IPv4(192, 168, 1, 50)},
	}
	pktSrv := &Server{advertise: adv}
	pkt, err := pktSrv.buildAnnouncement()
	if err != nil {
		t.Fatalf("buildAnnouncement: %v", err)
	}

	var msg dnsmessage.Message
	if err := msg.Unpack(pkt); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if !msg.Header.Response {
		t.Fatalf("announcement is not a response")
	}

	var got []Entry
	rxSrv := &Server{opts: Options{OnEntry: func(e Entry) { got = append(got, e) }}}
	rxSrv.handleResponse(&msg)

	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.Instance != "peerbox" {
		t.Errorf("instance = %q, want peerbox", e.Instance)
	}
	if e.Host != "peerbox.local" {
		t.Errorf("host = %q, want peerbox.local", e.Host)
	}
	if e.Port != 22 {
		t.Errorf("port = %d, want 22", e.Port)
	}
	if len(e.Addrs) != 1 || e.Addrs[0].String() != "192.168.1.50" {
		t.Errorf("addrs = %v, want [192.168.1.50]", e.Addrs)
	}
	if len(e.Text) != 2 || e.Text[0] != "wash=1" {
		t.Errorf("text = %v, want [wash=1 v=0.9.0]", e.Text)
	}
}

// TestQueryIsValidPTR confirms the browse query packs as a PTR question
// for the wash service and is a query (not a response).
func TestQueryIsValidPTR(t *testing.T) {
	q := dnsmessage.Message{
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(fqService()),
			Type:  dnsmessage.TypePTR,
			Class: dnsmessage.ClassINET,
		}},
	}
	pkt, err := q.Pack()
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(pkt); err != nil {
		t.Fatalf("unpack query: %v", err)
	}
	if msg.Header.Response {
		t.Fatalf("query packed as a response")
	}
	if len(msg.Questions) != 1 || msg.Questions[0].Type != dnsmessage.TypePTR {
		t.Fatalf("want one PTR question, got %+v", msg.Questions)
	}
	if msg.Questions[0].Name.String() != "_wash._tcp.local." {
		t.Errorf("question name = %q", msg.Questions[0].Name.String())
	}
}

// TestForeignServiceIgnored proves a non-wash announcement sharing the
// multicast group (a printer, a Spotify device) does NOT leak into Entries
// — regression for the live-LAN bug where the fallback loop emitted any
// SRV-bearing instance regardless of service type.
func TestForeignServiceIgnored(t *testing.T) {
	mkName := dnsmessage.MustNewName
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{Response: true, Authoritative: true},
		Answers: []dnsmessage.Resource{
			// A foreign service: SRV + A, no _wash PTR.
			{
				Header: dnsmessage.ResourceHeader{Name: mkName("printer._http._tcp.local."), Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET, TTL: 120},
				Body:   &dnsmessage.SRVResource{Port: 80, Target: mkName("printer.local.")},
			},
			{
				Header: dnsmessage.ResourceHeader{Name: mkName("printer.local."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 120},
				Body:   &dnsmessage.AResource{A: [4]byte{172, 16, 16, 142}},
			},
			// A real wash peer in the same packet.
			{
				Header: dnsmessage.ResourceHeader{Name: mkName(fqService()), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: 120},
				Body:   &dnsmessage.PTRResource{PTR: mkName("realbox._wash._tcp.local.")},
			},
			{
				Header: dnsmessage.ResourceHeader{Name: mkName("realbox._wash._tcp.local."), Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET, TTL: 120},
				Body:   &dnsmessage.SRVResource{Port: 22, Target: mkName("realbox.local.")},
			},
			{
				Header: dnsmessage.ResourceHeader{Name: mkName("realbox.local."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 120},
				Body:   &dnsmessage.AResource{A: [4]byte{172, 16, 17, 5}},
			},
		},
	}
	var got []Entry
	srv := &Server{opts: Options{OnEntry: func(e Entry) { got = append(got, e) }}}
	srv.handleResponse(&msg)

	if len(got) != 1 {
		t.Fatalf("want only the wash peer, got %d: %+v", len(got), got)
	}
	if got[0].Instance != "realbox" {
		t.Errorf("emitted wrong instance %q (foreign service leaked?)", got[0].Instance)
	}
}

func TestTrimService(t *testing.T) {
	cases := map[string]string{
		"peerbox._wash._tcp.local.": "peerbox",
		"PeerBox._wash._tcp.local.": "peerbox", // lowercased
		"bare.":                     "bare",
	}
	for in, want := range cases {
		if got := trimService(in); got != want {
			t.Errorf("trimService(%q) = %q, want %q", in, got, want)
		}
	}
}
