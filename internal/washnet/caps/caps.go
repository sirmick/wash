// Package caps describes what a backend can do: which object kinds it renders
// and which features it supports. Capabilities serve double duty (docs/NET.md
// §2.7): they gate the runtime UI (grey out the unsupported) and they drive
// validation (config that exceeds a backend's caps yields a diagnostic, never a
// silent drop).
package caps

import "github.com/sirmick/wash/internal/washnet/model"

// Feature is a coarse capability flag, orthogonal to object kinds (e.g. a
// backend may render firewall zones structurally but not actually enforce NAT).
type Feature string

const (
	CanAP            Feature = "ap"
	CanDHCPServer    Feature = "dhcp-server"
	CanNAT           Feature = "nat"
	CanZones         Feature = "zones"
	CanWireGuard     Feature = "wireguard"
	CanVLAN          Feature = "vlan"
	CanBridge        Feature = "bridge"
	CanPolicyRouting Feature = "policy-routing"
)

// AllFeatures is every defined feature (the openwrt/UCI profile has them all).
var AllFeatures = []Feature{
	CanAP, CanDHCPServer, CanNAT, CanZones, CanWireGuard, CanVLAN, CanBridge, CanPolicyRouting,
}

// Capabilities is the set of object kinds (package/section keys) and features a
// backend covers.
type Capabilities struct {
	Kinds    map[string]bool
	Features map[Feature]bool
}

// SupportsKind reports whether the backend renders the given package/section.
func (c Capabilities) SupportsKind(pkg, section string) bool {
	return c.Kinds[pkg+"/"+section]
}

// Has reports whether the backend supports a feature.
func (c Capabilities) Has(f Feature) bool { return c.Features[f] }

// Full covers every object kind and feature — the openwrt/UCI profile, where
// nothing is greyed out.
func Full() Capabilities {
	c := Capabilities{Kinds: map[string]bool{}, Features: map[Feature]bool{}}
	for _, k := range model.Kinds() {
		c.Kinds[k] = true
	}
	for _, f := range AllFeatures {
		c.Features[f] = true
	}
	return c
}

// New builds a Capabilities from explicit kind keys and features.
func New(kinds []string, features ...Feature) Capabilities {
	c := Capabilities{Kinds: map[string]bool{}, Features: map[Feature]bool{}}
	for _, k := range kinds {
		c.Kinds[k] = true
	}
	for _, f := range features {
		c.Features[f] = true
	}
	return c
}
