// Package validate is wash's server-authoritative schema check (docs/NET.md
// §2.5, §4.2). A3 covers field-level checks (required, enum, port/MAC datatypes)
// and capability gating (config that exceeds the active backend's caps). The
// relational invariants — references, overlaps, shadowing — land in A4.
//
// Every Diagnostic carries a Path so the FE can map it onto a widget.
package validate

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

type Severity int

const (
	Error Severity = iota
	Warning
)

func (s Severity) String() string {
	if s == Warning {
		return "warning"
	}
	return "error"
}

// Diagnostic is one validation finding, located by Path (e.g.
// "Interfaces[0].Proto.IPAddr"). JSON-tagged for the FE wire.
type Diagnostic struct {
	Path     string   `json:"path"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

// MarshalJSON renders Severity as "error"/"warning" on the wire.
func (s Severity) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

var (
	policyTargets = []string{"ACCEPT", "REJECT", "DROP"}
	ruleTargets   = []string{"ACCEPT", "REJECT", "DROP", "NOTRACK", "MARK"}
	redirTargets  = []string{"DNAT", "SNAT"}
	natTargets    = []string{"SNAT", "MASQUERADE"}
	wifiModes     = []string{"ap", "sta", "mesh", "adhoc", "monitor"}
	wifiBands     = []string{"2g", "5g", "6g", "60g"}
	deviceTypes   = []string{"bridge", "8021q", "8021ad", "macvlan", "veth"}
)

// Validate runs field-level checks and capability gating over a Config, given
// the active backend's Capabilities. Pure and total.
func Validate(c model.Config, cp caps.Capabilities) []Diagnostic {
	var d diags
	gateKind(&d, c.Interfaces, "Interfaces", cp)
	gateKind(&d, c.Devices, "Devices", cp)
	gateKind(&d, c.Routes, "Routes", cp)
	gateKind(&d, c.PolicyRules, "PolicyRules", cp)
	gateKind(&d, c.WGPeers, "WGPeers", cp)
	gateKind(&d, c.FwDefaults, "FwDefaults", cp)
	gateKind(&d, c.Zones, "Zones", cp)
	gateKind(&d, c.Forwardings, "Forwardings", cp)
	gateKind(&d, c.Redirects, "Redirects", cp)
	gateKind(&d, c.FwRules, "FwRules", cp)
	gateKind(&d, c.NATs, "NATs", cp)
	gateKind(&d, c.IPSets, "IPSets", cp)
	gateKind(&d, c.Dnsmasq, "Dnsmasq", cp)
	gateKind(&d, c.Pools, "Pools", cp)
	gateKind(&d, c.Hosts, "Hosts", cp)
	gateKind(&d, c.Domains, "Domains", cp)
	gateKind(&d, c.CNAMEs, "CNAMEs", cp)
	gateKind(&d, c.Radios, "Radios", cp)
	gateKind(&d, c.SSIDs, "SSIDs", cp)

	for i, o := range c.Interfaces {
		d.iface(o, at("Interfaces", i), cp)
	}
	for i, o := range c.Devices {
		d.device(o, at("Devices", i), cp)
	}
	for i := range c.PolicyRules {
		d.feature(at("PolicyRules", i), cp, caps.CanPolicyRouting, "policy routing")
	}
	for i, o := range c.Zones {
		d.zone(o, at("Zones", i), cp)
	}
	for i, o := range c.Redirects {
		d.redirect(o, at("Redirects", i))
	}
	for i, o := range c.FwRules {
		d.fwRule(o, at("FwRules", i))
	}
	for i, o := range c.NATs {
		d.enum(at("NATs", i), "Target", o.Target, natTargets)
	}
	for i := range c.Pools {
		d.feature(at("Pools", i), cp, caps.CanDHCPServer, "DHCP server")
	}
	for i, o := range c.Hosts {
		d.host(o, at("Hosts", i))
	}
	for i, o := range c.Radios {
		d.radio(o, at("Radios", i))
	}
	for i, o := range c.SSIDs {
		d.ssid(o, at("SSIDs", i), cp)
	}
	d.relational(c)
	return d
}

// --- per-object field checks ----------------------------------------------

func (d *diags) iface(o model.Interface, path string, cp caps.Capabilities) {
	d.required(path, "Name", o.Name)
	switch p := o.Proto.(type) {
	case model.StaticProto:
		if !p.Primary().IsValid() {
			d.add(path+".Proto.IPAddr", "required", "static interface needs an ipaddr", Error)
		}
	case model.PPPoEProto:
		d.required(path+".Proto", "Username", p.Username)
	case model.WireGuardProto:
		d.feature(path, cp, caps.CanWireGuard, "WireGuard")
		d.required(path+".Proto", "PrivateKey", p.PrivateKey)
	}
}

func (d *diags) device(o model.Device, path string, cp caps.Capabilities) {
	d.required(path, "Name", o.Name)
	d.enum(path, "Type", o.Type, deviceTypes)
	switch o.Type {
	case "bridge":
		d.feature(path, cp, caps.CanBridge, "bridges")
	case "8021q", "8021ad":
		d.feature(path, cp, caps.CanVLAN, "VLANs")
	}
}

func (d *diags) zone(o model.Zone, path string, cp caps.Capabilities) {
	d.feature(path, cp, caps.CanZones, "firewall zones")
	d.required(path, "Name", o.Name)
	d.enum(path, "Input", o.Input, policyTargets)
	d.enum(path, "Output", o.Output, policyTargets)
	d.enum(path, "Forward", o.Forward, policyTargets)
	if o.Masq {
		d.feature(path, cp, caps.CanNAT, "NAT/masquerade")
	}
}

func (d *diags) redirect(o model.Redirect, path string) {
	d.required(path, "Name", o.Name)
	d.enum(path, "Target", o.Target, redirTargets)
	d.port(path, "SrcDPort", o.SrcDPort)
	d.port(path, "DestPort", o.DestPort)
}

func (d *diags) fwRule(o model.FirewallRule, path string) {
	d.enum(path, "Target", o.Target, ruleTargets)
	d.port(path, "SrcPort", o.SrcPort)
	d.port(path, "DestPort", o.DestPort)
}

func (d *diags) host(o model.Host, path string) {
	if o.MAC != "" && !validMAC(o.MAC) {
		d.add(path+".MAC", "invalid_mac", fmt.Sprintf("%q is not a valid MAC address", o.MAC), Error)
	}
}

func (d *diags) radio(o model.WifiDevice, path string) {
	d.required(path, "Name", o.Name)
	d.enum(path, "Band", o.Band, wifiBands)
	if o.Channel != "" && o.Channel != "auto" {
		if _, err := strconv.Atoi(o.Channel); err != nil {
			d.add(path+".Channel", "invalid_channel", `channel must be "auto" or a number`, Error)
		}
	}
}

func (d *diags) ssid(o model.WifiIface, path string, cp caps.Capabilities) {
	d.enum(path, "Mode", o.Mode, wifiModes)
	if o.Mode == "ap" {
		d.feature(path, cp, caps.CanAP, "hosting an access point")
		d.required(path, "SSID", o.SSID)
	}
}

// --- helpers ---------------------------------------------------------------

type diags []Diagnostic

func (d *diags) add(path, code, msg string, sev Severity) {
	*d = append(*d, Diagnostic{Path: path, Code: code, Message: msg, Severity: sev})
}

func (d *diags) required(path, field, val string) {
	if val == "" {
		d.add(path+"."+field, "required", field+" is required", Error)
	}
}

func (d *diags) enum(path, field, val string, allowed []string) {
	if val == "" {
		return
	}
	for _, a := range allowed {
		if val == a {
			return
		}
	}
	d.add(path+"."+field, "invalid_enum", fmt.Sprintf("%q must be one of %s", val, strings.Join(allowed, ", ")), Error)
}

func (d *diags) port(path, field, spec string) {
	if !validPortSpec(spec) {
		d.add(path+"."+field, "invalid_port", fmt.Sprintf("%q is not a valid port/range/list", spec), Error)
	}
}

func (d *diags) feature(path string, cp caps.Capabilities, f caps.Feature, what string) {
	if !cp.Has(f) {
		d.add(path, "unsupported_feature", fmt.Sprintf("%s is not supported by the active backend (needs %s)", what, f), Error)
	}
}

// gateKind emits an unsupported_kind diagnostic for any object whose kind the
// active backend does not render.
func gateKind[T model.Kinded](d *diags, slice []T, field string, cp caps.Capabilities) {
	for i, o := range slice {
		if !cp.SupportsKind(o.UCIPackage(), o.UCISection()) {
			d.add(at(field, i), "unsupported_kind",
				fmt.Sprintf("%s is not supported by the active backend", o.UCISection()), Error)
		}
	}
}

func at(field string, i int) string { return fmt.Sprintf("%s[%d]", field, i) }

func validMAC(s string) bool { _, err := net.ParseMAC(s); return err == nil }

func validPortSpec(s string) bool {
	if s == "" {
		return true
	}
	for _, tok := range strings.Fields(s) {
		if lo, hi, ok := strings.Cut(tok, "-"); ok {
			if !validPort(lo) || !validPort(hi) {
				return false
			}
		} else if !validPort(tok) {
			return false
		}
	}
	return true
}

func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}
