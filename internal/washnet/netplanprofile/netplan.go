// Package netplanprofile compiles the wash UCI model to/from netplan YAML
// (network: version 2). It is the netplan counterpart of networkdprofile /
// nmprofile: Render(model.Config) → a single netplan document, Parse(YAML) →
// model.Config, with Render∘Parse and Parse∘Render asserted as identity over
// the shared golden corpus.
//
// netplan groups by device class — ethernets / bridges / vlans / tunnels
// (wireguard) / wifis — in one document, unlike the per-link unit files
// networkd uses. yaml.v3 marshals struct fields in declaration order and map
// keys alphabetically, so output is deterministic.
package netplanprofile

// FileName is the single file wash owns under /etc/netplan (takeover: wash is
// the sole author). The 00- prefix sorts it first; netplan merges by filename.
const FileName = "00-wash.yaml"

// doc is the top-level netplan document.
type doc struct {
	Network network `yaml:"network"`
}

type network struct {
	Version   int               `yaml:"version"`
	Renderer  string            `yaml:"renderer,omitempty"`
	Ethernets map[string]device `yaml:"ethernets,omitempty"`
	Bridges   map[string]device `yaml:"bridges,omitempty"`
	Vlans     map[string]device `yaml:"vlans,omitempty"`
	Tunnels   map[string]device `yaml:"tunnels,omitempty"`
	Wifis     map[string]device `yaml:"wifis,omitempty"`
}

// device is the union of every netplan device class's keys. omitempty keeps a
// rendered entry to just the fields its class uses.
type device struct {
	DHCP4       *bool        `yaml:"dhcp4,omitempty"`
	DHCP6       *bool        `yaml:"dhcp6,omitempty"`
	Addresses   []string     `yaml:"addresses,omitempty"`
	Nameservers *nameservers `yaml:"nameservers,omitempty"`
	Routes      []nproute    `yaml:"routes,omitempty"`
	// bridges
	Interfaces []string `yaml:"interfaces,omitempty"`
	// vlans
	ID   *int   `yaml:"id,omitempty"`
	Link string `yaml:"link,omitempty"`
	// tunnels (wireguard)
	Mode  string   `yaml:"mode,omitempty"`
	Key   string   `yaml:"key,omitempty"`
	Port  *int     `yaml:"port,omitempty"`
	Peers []wgpeer `yaml:"peers,omitempty"`
	// wifis
	AccessPoints map[string]accesspoint `yaml:"access-points,omitempty"`
}

type nameservers struct {
	Addresses []string `yaml:"addresses,omitempty"`
	Search    []string `yaml:"search,omitempty"`
}

type nproute struct {
	To     string `yaml:"to"`
	Via    string `yaml:"via,omitempty"`
	Metric *int   `yaml:"metric,omitempty"`
	Table  string `yaml:"table,omitempty"`
}

type wgpeer struct {
	Keys       wgkeys   `yaml:"keys"`
	AllowedIPs []string `yaml:"allowed-ips,omitempty"`
	Endpoint   string   `yaml:"endpoint,omitempty"`
	Keepalive  *int     `yaml:"keepalive,omitempty"`
}

type wgkeys struct {
	Public string `yaml:"public"`
	Shared string `yaml:"shared,omitempty"`
}

type accesspoint struct {
	Password string `yaml:"password,omitempty"`
	Hidden   bool   `yaml:"hidden,omitempty"`
}

func boolp(b bool) *bool { return &b }
func intp(i int) *int    { return &i }
