// Package model defines the UCI-shaped object vocabulary that is wash-net's
// single source of truth. Each object maps ~1:1 to a UCI section; the `uci`
// struct tags carry the serialization mapping (see internal/washnet/codec),
// and field types carry datatype validation for free. See docs/NET.md §4.
//
// Tag grammar (consumed by the codec):
//
//	uci:",name"          this field is the UCI section NAME (config <type> 'NAME')
//	uci:"opt"            option opt
//	uci:"opt,list"       repeated `list opt '…'`
//	uci:"opt,union"      tagged union: opt holds the discriminator, the concrete
//	                     variant's own fields are inlined into the same section
//	uci:"-" / (no tag)   ignored
//
// Sum types (proto, encryption) are modeled as a discriminator interface + one
// struct per variant, flattened into the flat UCI section by the codec. This is
// the documented Go "sum-type tax" — repaid by driving form conditional
// visibility off the variant structs in the FE.
package model

import (
	"net/netip"
	"reflect"
)

// Config is the full declarative network configuration. Pure data; validation,
// rendering, and application happen in sibling packages. Field order also
// determines render order within a UCI package file.
type Config struct {
	// network
	Globals     []Globals
	Interfaces  []Interface
	Devices     []Device
	Routes      []Route
	PolicyRules []PolicyRule
	WGPeers     []WGPeer
	// firewall
	FwDefaults  []Defaults
	Zones       []Zone
	Forwardings []Forwarding
	Redirects   []Redirect
	FwRules     []FirewallRule
	NATs        []NAT
	IPSets      []IPSet
	// dhcp
	Dnsmasq []Dnsmasq
	Pools   []DHCPPool
	Hosts   []Host
	Domains []Domain
	CNAMEs  []CNAME
	// wireless
	Radios []WifiDevice
	SSIDs  []WifiIface
}

// --- network ---------------------------------------------------------------

type Globals struct {
	Name      string       `uci:",name"` // conventionally 'globals'
	ULAPrefix netip.Prefix `uci:"ula_prefix"`
}

func (Globals) UCIPackage() string { return "network" }
func (Globals) UCISection() string { return "globals" }

// Interface is a named network interface (config interface 'lan'). Its
// addressing is carried by the proto union.
type Interface struct {
	Name   string      `uci:",name"`
	Device string      `uci:"device"`
	Proto  ProtoConfig `uci:"proto,union"`
}

func (Interface) UCIPackage() string { return "network" }
func (Interface) UCISection() string { return "interface" }

// ProtoConfig is the interface-protocol union discriminator.
type ProtoConfig interface{ UCITag() string }

type NoneProto struct{}

func (NoneProto) UCITag() string { return "none" }

type StaticProto struct {
	IPAddr  netip.Prefix `uci:"ipaddr"`
	Gateway netip.Addr   `uci:"gateway"`
	DNS     []netip.Addr `uci:"dns,list"`
}

func (StaticProto) UCITag() string { return "static" }

type DHCPProto struct {
	Hostname string `uci:"hostname"`
}

func (DHCPProto) UCITag() string { return "dhcp" }

type DHCPv6Proto struct{}

func (DHCPv6Proto) UCITag() string { return "dhcpv6" }

type PPPoEProto struct {
	Username string `uci:"username"`
	Password string `uci:"password"`
}

func (PPPoEProto) UCITag() string { return "pppoe" }

type WireGuardProto struct {
	PrivateKey string         `uci:"private_key"`
	ListenPort int            `uci:"listen_port"`
	Addresses  []netip.Prefix `uci:"addresses,list"`
}

func (WireGuardProto) UCITag() string { return "wireguard" }

// Device is an anonymous section whose `name` option is the resulting ifname
// (e.g. br-lan).
type Device struct {
	Name  string   `uci:"name"`
	Type  string   `uci:"type"` // bridge, 8021q, macvlan
	Ports []string `uci:"ports,list"`
	MTU   int      `uci:"mtu"`
}

func (Device) UCIPackage() string { return "network" }
func (Device) UCISection() string { return "device" }

type Route struct {
	Interface string       `uci:"interface"`
	Target    netip.Prefix `uci:"target"`
	Gateway   netip.Addr   `uci:"gateway"`
	Metric    int          `uci:"metric"`
	Table     string       `uci:"table"`
}

func (Route) UCIPackage() string { return "network" }
func (Route) UCISection() string { return "route" }

// PolicyRule is a `config rule` in the network package (ip rule).
type PolicyRule struct {
	In       string       `uci:"in"`
	Out      string       `uci:"out"`
	Src      netip.Prefix `uci:"src"`
	Dest     netip.Prefix `uci:"dest"`
	Lookup   string       `uci:"lookup"`
	Priority int          `uci:"priority"`
}

func (PolicyRule) UCIPackage() string { return "network" }
func (PolicyRule) UCISection() string { return "rule" }

// WGPeer is wash-normalized (section `wireguard_peer` + an Interface ref); the
// UCI adapter maps it to OpenWRT's `wireguard_<ifname>` section in Phase D.
type WGPeer struct {
	Name                string         `uci:",name"`
	Interface           string         `uci:"interface"`
	PublicKey           string         `uci:"public_key"`
	PresharedKey        string         `uci:"preshared_key"`
	AllowedIPs          []netip.Prefix `uci:"allowed_ips,list"`
	EndpointHost        string         `uci:"endpoint_host"`
	EndpointPort        int            `uci:"endpoint_port"`
	PersistentKeepalive int            `uci:"persistent_keepalive"`
	RouteAllowedIPs     bool           `uci:"route_allowed_ips"`
}

func (WGPeer) UCIPackage() string { return "network" }
func (WGPeer) UCISection() string { return "wireguard_peer" }

// --- firewall --------------------------------------------------------------

type Defaults struct {
	Input    string `uci:"input"`
	Output   string `uci:"output"`
	Forward  string `uci:"forward"`
	SynFlood bool   `uci:"syn_flood"`
}

func (Defaults) UCIPackage() string { return "firewall" }
func (Defaults) UCISection() string { return "defaults" }

type Zone struct {
	Name     string   `uci:"name"`
	Networks []string `uci:"network,list"`
	Input    string   `uci:"input"`
	Output   string   `uci:"output"`
	Forward  string   `uci:"forward"`
	Masq     bool     `uci:"masq"`
	MTUFix   bool     `uci:"mtu_fix"`
}

func (Zone) UCIPackage() string { return "firewall" }
func (Zone) UCISection() string { return "zone" }

type Forwarding struct {
	Src  string `uci:"src"`
	Dest string `uci:"dest"`
}

func (Forwarding) UCIPackage() string { return "firewall" }
func (Forwarding) UCISection() string { return "forwarding" }

// Redirect is a DNAT / port-forward (config redirect).
type Redirect struct {
	Name     string     `uci:"name"`
	Src      string     `uci:"src"`
	SrcDPort string     `uci:"src_dport"`
	Dest     string     `uci:"dest"`
	DestIP   netip.Addr `uci:"dest_ip"`
	DestPort string     `uci:"dest_port"`
	Proto    string     `uci:"proto"`
	Target   string     `uci:"target"` // DNAT / SNAT
}

func (Redirect) UCIPackage() string { return "firewall" }
func (Redirect) UCISection() string { return "redirect" }

type FirewallRule struct {
	Name     string `uci:"name"`
	Src      string `uci:"src"`
	Dest     string `uci:"dest"`
	Proto    string `uci:"proto"`
	SrcPort  string `uci:"src_port"`
	DestPort string `uci:"dest_port"`
	Target   string `uci:"target"` // ACCEPT / REJECT / DROP
	Family   string `uci:"family"`
}

func (FirewallRule) UCIPackage() string { return "firewall" }
func (FirewallRule) UCISection() string { return "rule" }

type NAT struct {
	Name   string     `uci:"name"`
	Src    string     `uci:"src"`
	Target string     `uci:"target"` // SNAT / MASQUERADE
	SNATIP netip.Addr `uci:"snat_ip"`
}

func (NAT) UCIPackage() string { return "firewall" }
func (NAT) UCISection() string { return "nat" }

type IPSet struct {
	Name  string   `uci:"name"`
	Match string   `uci:"match"`
	Entry []string `uci:"entry,list"`
}

func (IPSet) UCIPackage() string { return "firewall" }
func (IPSet) UCISection() string { return "ipset" }

// --- dhcp ------------------------------------------------------------------

type Dnsmasq struct {
	DomainNeeded bool     `uci:"domainneeded"`
	BogusPriv    bool     `uci:"boguspriv"`
	Local        string   `uci:"local"`
	Domain       string   `uci:"domain"`
	ExpandHosts  bool     `uci:"expandhosts"`
	Server       []string `uci:"server,list"`
}

func (Dnsmasq) UCIPackage() string { return "dhcp" }
func (Dnsmasq) UCISection() string { return "dnsmasq" }

// DHCPPool is named by its interface (config dhcp 'lan').
type DHCPPool struct {
	Name      string `uci:",name"`
	Interface string `uci:"interface"`
	Start     int    `uci:"start"`
	Limit     int    `uci:"limit"`
	LeaseTime string `uci:"leasetime"`
	Ignore    bool   `uci:"ignore"`
	RA        string `uci:"ra"`
	DHCPv6    string `uci:"dhcpv6"`
}

func (DHCPPool) UCIPackage() string { return "dhcp" }
func (DHCPPool) UCISection() string { return "dhcp" }

// Host is a static lease (config host).
type Host struct {
	Name      string     `uci:"name"`
	MAC       string     `uci:"mac"`
	IP        netip.Addr `uci:"ip"`
	Hostname  string     `uci:"hostname"`
	LeaseTime string     `uci:"leasetime"`
}

func (Host) UCIPackage() string { return "dhcp" }
func (Host) UCISection() string { return "host" }

// Domain is a static DNS A record (config domain).
type Domain struct {
	Name string     `uci:"name"`
	IP   netip.Addr `uci:"ip"`
}

func (Domain) UCIPackage() string { return "dhcp" }
func (Domain) UCISection() string { return "domain" }

type CNAME struct {
	Alias  string `uci:"cname"`
	Target string `uci:"target"`
}

func (CNAME) UCIPackage() string { return "dhcp" }
func (CNAME) UCISection() string { return "cname" }

// --- wireless --------------------------------------------------------------

// WifiDevice is a radio (config wifi-device 'radio0').
type WifiDevice struct {
	Name     string `uci:",name"`
	Type     string `uci:"type"`
	Band     string `uci:"band"` // 2g / 5g / 6g
	Channel  string `uci:"channel"`
	HTMode   string `uci:"htmode"`
	Country  string `uci:"country"`
	Disabled bool   `uci:"disabled"`
}

func (WifiDevice) UCIPackage() string { return "wireless" }
func (WifiDevice) UCISection() string { return "wifi-device" }

// WifiIface is an SSID (config wifi-iface). Encryption is a union.
type WifiIface struct {
	Name       string     `uci:",name"`
	Device     string     `uci:"device"`
	Mode       string     `uci:"mode"` // ap / sta / mesh
	SSID       string     `uci:"ssid"`
	Network    string     `uci:"network"`
	Encryption Encryption `uci:"encryption,union"`
	Hidden     bool       `uci:"hidden"`
	Isolate    bool       `uci:"isolate"`
}

func (WifiIface) UCIPackage() string { return "wireless" }
func (WifiIface) UCISection() string { return "wifi-iface" }

// Encryption is the wifi encryption union.
type Encryption interface{ UCITag() string }

type EncNone struct{}

func (EncNone) UCITag() string { return "none" }

type EncPSK2 struct {
	Key string `uci:"key"`
}

func (EncPSK2) UCITag() string { return "psk2" }

type EncSAE struct {
	Key string `uci:"key"`
}

func (EncSAE) UCITag() string { return "sae" }

// --- union registry --------------------------------------------------------

var unionVariants = map[reflect.Type]map[string]reflect.Type{}

func registerUnion[T any](variants ...interface{ UCITag() string }) {
	iface := reflect.TypeOf((*T)(nil)).Elem()
	m := unionVariants[iface]
	if m == nil {
		m = map[string]reflect.Type{}
		unionVariants[iface] = m
	}
	for _, v := range variants {
		m[v.UCITag()] = reflect.TypeOf(v)
	}
}

// VariantType resolves a union interface type + discriminator tag to the
// concrete variant struct type. Used by the codec on parse.
func VariantType(iface reflect.Type, tag string) (reflect.Type, bool) {
	m, ok := unionVariants[iface]
	if !ok {
		return nil, false
	}
	t, ok := m[tag]
	return t, ok
}

func init() {
	registerUnion[ProtoConfig](NoneProto{}, StaticProto{}, DHCPProto{}, DHCPv6Proto{}, PPPoEProto{}, WireGuardProto{})
	registerUnion[Encryption](EncNone{}, EncPSK2{}, EncSAE{})
}
