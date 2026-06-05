// Package model defines the UCI-shaped object vocabulary that is wash-net's
// single source of truth. Each object maps ~1:1 to a UCI section; the `uci`
// struct tags carry the serialization mapping (see internal/washnet/codec) and
// the `ui` tags carry presentation hints consumed by the UI codegen (see
// internal/washnet/uigen). Field types carry datatype validation for free. See
// docs/NET.md §4, §6.
//
// uci tag grammar:
//
//	uci:",name"          this field is the UCI section NAME (config <type> 'NAME')
//	uci:"opt"            option opt
//	uci:"opt,list"       repeated `list opt '…'`
//	uci:"opt,union"      tagged union: opt holds the discriminator, the concrete
//	                     variant's own fields are inlined into the same section
//	uci:"-" / (no tag)   ignored
//
// ui tag grammar (all optional; widget otherwise derives from the field type):
//
//	ui:"group=g"         logical form group
//	ui:"ref=kind"        render as a picker of existing objects of that kind
//	ui:"widget=w"        override the derived widget (e.g. mac)
package model

import (
	"net/netip"
	"reflect"
	"sort"
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
	Name      string       `uci:",name"`
	ULAPrefix netip.Prefix `uci:"ula_prefix" ui:"group=ipv6"`
}

func (Globals) UCIPackage() string { return "network" }
func (Globals) UCISection() string { return "globals" }

// Interface is a named network interface (config interface 'lan'). Its
// addressing is carried by the proto union.
type Interface struct {
	Name   string      `uci:",name"`
	Device string      `uci:"device" ui:"group=general,ref=device"`
	Proto  ProtoConfig `uci:"proto,union" ui:"group=general"`
	// IP6Assign delegates a /N prefix to this interface from an upstream PD
	// (OpenWRT's `option ip6assign '60'`) — the standard router-LAN IPv6 pattern.
	// Interface-level (independent of proto). Zero = unset/omitted. UCI-only; the
	// link-renderer backends (NM/networkd/netplan) ignore it.
	IP6Assign int `uci:"ip6assign" ui:"group=ipv6"`
}

func (Interface) UCIPackage() string { return "network" }
func (Interface) UCISection() string { return "interface" }

// ProtoConfig is the interface-protocol union discriminator.
type ProtoConfig interface{ UCITag() string }

type NoneProto struct{}

func (NoneProto) UCITag() string { return "none" }

type StaticProto struct {
	IPAddr   netip.Prefix `uci:"ipaddr" ui:"group=addressing"`
	Gateway  netip.Addr   `uci:"gateway" ui:"group=addressing"`
	IP6Addr  netip.Prefix `uci:"ip6addr" ui:"group=addressing"`
	IP6Gw    netip.Addr   `uci:"ip6gw" ui:"group=addressing"`
	DNS      []netip.Addr `uci:"dns,list" ui:"group=addressing"`
}

func (StaticProto) UCITag() string { return "static" }

type DHCPProto struct {
	IPv4     bool   `uci:"ipv4"`     // request a DHCPv4 lease
	IPv6     bool   `uci:"ipv6"`     // request DHCPv6 / accept SLAAC
	Hostname string `uci:"hostname"` // hostname sent to the DHCP server
}

func (DHCPProto) UCITag() string { return "dhcp" }

type PPPoEProto struct {
	Username string `uci:"username"`
	Password string `uci:"password" ui:"widget=password"`
}

func (PPPoEProto) UCITag() string { return "pppoe" }

type WireGuardProto struct {
	PrivateKey string         `uci:"private_key" ui:"widget=password"`
	ListenPort int            `uci:"listen_port"`
	Addresses  []netip.Prefix `uci:"addresses,list"`
}

func (WireGuardProto) UCITag() string { return "wireguard" }

// Device is an anonymous section whose `name` option is the resulting ifname
// (e.g. br-lan).
type Device struct {
	Name   string   `uci:"name"`
	Type   string   `uci:"type"`
	Ports  []string `uci:"ports,list"` // bridge members
	Ifname string   `uci:"ifname"`     // vlan/macvlan parent link
	VID    int      `uci:"vid"`        // 802.1q vlan id
	MTU    int      `uci:"mtu"`
}

func (Device) UCIPackage() string { return "network" }
func (Device) UCISection() string { return "device" }

type Route struct {
	Interface string       `uci:"interface" ui:"ref=interface"`
	Target    netip.Prefix `uci:"target"`
	Gateway   netip.Addr   `uci:"gateway"`
	Metric    int          `uci:"metric"`
	Table     string       `uci:"table"`
}

func (Route) UCIPackage() string { return "network" }
func (Route) UCISection() string { return "route" }

// PolicyRule is a `config rule` in the network package (ip rule).
type PolicyRule struct {
	In       string       `uci:"in" ui:"ref=interface"`
	Out      string       `uci:"out" ui:"ref=interface"`
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
	Interface           string         `uci:"interface" ui:"ref=interface"`
	PublicKey           string         `uci:"public_key"`
	PresharedKey        string         `uci:"preshared_key" ui:"widget=password"`
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
	Networks []string `uci:"network,list" ui:"ref=interface"`
	Input    string   `uci:"input"`
	Output   string   `uci:"output"`
	Forward  string   `uci:"forward"`
	Masq     bool     `uci:"masq"`
	MTUFix   bool     `uci:"mtu_fix"`
}

func (Zone) UCIPackage() string { return "firewall" }
func (Zone) UCISection() string { return "zone" }

type Forwarding struct {
	Src  string `uci:"src" ui:"ref=zone"`
	Dest string `uci:"dest" ui:"ref=zone"`
}

func (Forwarding) UCIPackage() string { return "firewall" }
func (Forwarding) UCISection() string { return "forwarding" }

// Redirect is a DNAT / port-forward (config redirect).
type Redirect struct {
	Name     string     `uci:"name"`
	Src      string     `uci:"src" ui:"ref=zone"`
	SrcDPort string     `uci:"src_dport"`
	Dest     string     `uci:"dest" ui:"ref=zone"`
	DestIP   netip.Addr `uci:"dest_ip"`
	DestPort string     `uci:"dest_port"`
	Proto    string     `uci:"proto"`
	Target   string     `uci:"target"`
}

func (Redirect) UCIPackage() string { return "firewall" }
func (Redirect) UCISection() string { return "redirect" }

type FirewallRule struct {
	Name     string `uci:"name"`
	Src      string `uci:"src" ui:"ref=zone"`
	Dest     string `uci:"dest" ui:"ref=zone"`
	Proto    string `uci:"proto"`
	SrcPort  string `uci:"src_port"`
	DestPort string `uci:"dest_port"`
	Target   string `uci:"target"`
	Family   string `uci:"family"`
}

func (FirewallRule) UCIPackage() string { return "firewall" }
func (FirewallRule) UCISection() string { return "rule" }

type NAT struct {
	Name   string     `uci:"name"`
	Src    string     `uci:"src" ui:"ref=zone"`
	Target string     `uci:"target"`
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
	Interface string `uci:"interface" ui:"ref=interface"`
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
	MAC       string     `uci:"mac" ui:"widget=mac"`
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
	Band     string `uci:"band"`
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
	Device     string     `uci:"device" ui:"ref=radio"`
	Mode       string     `uci:"mode"`
	SSID       string     `uci:"ssid"`
	Network    string     `uci:"network" ui:"ref=interface"`
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
	Key string `uci:"key" ui:"widget=password"`
}

func (EncPSK2) UCITag() string { return "psk2" }

type EncSAE struct {
	Key string `uci:"key" ui:"widget=password"`
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

// VariantsOf returns every variant struct type registered for a union
// interface, sorted by discriminator tag for deterministic output.
func VariantsOf(iface reflect.Type) []reflect.Type {
	m := unionVariants[iface]
	ts := make([]reflect.Type, 0, len(m))
	for _, t := range m {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return TagOf(ts[i]) < TagOf(ts[j]) })
	return ts
}

// TagOf returns the UCITag of a variant struct type.
func TagOf(t reflect.Type) string {
	if v, ok := reflect.New(t).Elem().Interface().(interface{ UCITag() string }); ok {
		return v.UCITag()
	}
	return ""
}

// Kinded is implemented by every object that maps to a UCI section.
type Kinded interface {
	UCIPackage() string
	UCISection() string
}

// Kinds returns the "package/section" key for every object kind, in Config
// field order. Used by caps to enumerate what a backend may cover.
func Kinds() []string {
	var ks []string
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		if ct.Field(i).Type.Kind() != reflect.Slice {
			continue
		}
		et := ct.Field(i).Type.Elem()
		if obj, ok := reflect.New(et).Elem().Interface().(Kinded); ok {
			ks = append(ks, obj.UCIPackage()+"/"+obj.UCISection())
		}
	}
	return ks
}

// ObjectTypes returns the element type of every Config slice field, in order —
// the set of model objects, for codegen.
func ObjectTypes() []reflect.Type {
	var ts []reflect.Type
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		if ct.Field(i).Type.Kind() != reflect.Slice {
			continue
		}
		et := ct.Field(i).Type.Elem()
		if _, ok := reflect.New(et).Elem().Interface().(Kinded); ok {
			ts = append(ts, et)
		}
	}
	return ts
}

func init() {
	registerUnion[ProtoConfig](NoneProto{}, StaticProto{}, DHCPProto{}, PPPoEProto{}, WireGuardProto{})
	registerUnion[Encryption](EncNone{}, EncPSK2{}, EncSAE{})
}
