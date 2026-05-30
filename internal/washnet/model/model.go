// Package model defines the UCI-shaped object vocabulary that is wash-net's
// single source of truth. Each object maps ~1:1 to a UCI section; the `uci`
// struct tags carry the serialization mapping (see internal/washnet/codec),
// and field types carry datatype validation for free. See docs/NET.md §4.
//
// A1 scope: Config + Interface + Zone, enough to prove the codec round-trip.
// The full vocabulary (dhcp/wireless/wireguard, proto/encryption unions) lands
// in A2.
package model

import "net/netip"

// Config is the full declarative network configuration. It is pure data; all
// validation, rendering, and application happen in sibling packages.
type Config struct {
	Interfaces []Interface
	Zones      []Zone
}

// Interface is a network interface (UCI: `config interface`). A1 carries a
// subset of fields; Proto is a plain string here and becomes a tagged union in
// A2.
type Interface struct {
	Name   string       `uci:",name"`
	Proto  string       `uci:"proto"`
	Device string       `uci:"device"`
	IPAddr netip.Prefix `uci:"ipaddr"`
	DNS    []netip.Addr `uci:"dns,list"`
}

// UCIPackage and UCISection locate this object within UCI's package/section
// namespace (`/etc/config/network` → `config interface`).
func (Interface) UCIPackage() string { return "network" }
func (Interface) UCISection() string { return "interface" }

// Zone is a firewall zone (UCI: `config zone`). A1 subset.
type Zone struct {
	Name     string   `uci:",name"`
	Networks []string `uci:"network,list"`
	Input    string   `uci:"input"`
	Output   string   `uci:"output"`
	Forward  string   `uci:"forward"`
	Masq     bool     `uci:"masq"`
}

func (Zone) UCIPackage() string { return "firewall" }
func (Zone) UCISection() string { return "zone" }
