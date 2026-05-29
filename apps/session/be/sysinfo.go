// System info shipped to the FE on session ready. The desktop banner
// renders hostname (FQDN), username, CPU + memory totals, and the
// list of external IPs. Read once at startup — none of these fields
// change frequently enough to warrant a refresh loop in v1.
//
// External IPs are best-effort: loopback, link-local, and Docker /
// virtual interfaces (docker0, br-*, veth*, tun/tap, wg*, virbr*) are
// filtered out so the user sees something close to "addresses other
// machines could reach me on." Both v4 and v6 are reported.

package session

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// sysInfo is the shape sent to the FE as the "system.info" app_msg.
// Field names match the wire keys; the FE picks them up by name.
type sysInfo struct {
	Hostname    string     `json:"hostname"`
	FQDN        string     `json:"fqdn"`
	Username    string     `json:"username"`
	CPUs        int        `json:"cpus"`
	MemBytes    uint64     `json:"mem_bytes"`
	Interfaces  []IfaceIPs `json:"interfaces"`
	Router      RouterInfo `json:"router"`
	SessionName string     `json:"session_name,omitempty"`
}

// RouterInfo describes the wash-router instance hosting this session.
// Populated from WASH_ROUTER_* env vars wash-router sets at startup;
// missing values stay empty and the FE hides the absent fields.
type RouterInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`
	Dev     bool   `json:"dev,omitempty"`
}

// IfaceIPs is a single network interface's addresses, grouped so the
// banner can render one line per iface ("br-lan: 172.16.16.60 2601:…").
// The BE deduplicates IPv6 within a group (one address per /64,
// preferring the EUI-64 stable form).
type IfaceIPs struct {
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

// gatherSysInfo collects every field once. Best-effort: any single
// failure falls back to a sensible empty value rather than refusing
// to ship the rest.
func gatherSysInfo() sysInfo {
	out := sysInfo{
		CPUs: runtime.NumCPU(),
	}
	if h, err := os.Hostname(); err == nil {
		out.Hostname = h
		out.FQDN = resolveFQDN(h)
	}
	if u, err := user.Current(); err == nil {
		out.Username = u.Username
	}
	out.MemBytes = readMemTotal()
	out.Interfaces = externalInterfaces()
	out.Router = RouterInfo{
		Version: os.Getenv("WASH_ROUTER_VERSION"),
		Commit:  os.Getenv("WASH_ROUTER_COMMIT"),
		Built:   os.Getenv("WASH_ROUTER_BUILT"),
		Dev:     os.Getenv("WASH_ROUTER_DEV") == "1",
	}
	out.SessionName = os.Getenv("WASH_SESSION_NAME")
	return out
}

// resolveFQDN tries a few strategies to produce hostname.searchdomain.
// Order:
//
//  1. If hostname already contains a dot, treat it as the FQDN.
//  2. Reverse-lookup the loopback addresses bound to hostname (often
//     populated by NSS hostname.* in /etc/hosts).
//  3. Read the search list from /etc/resolv.conf and append the first
//     entry. This is the fallback that works on most distros where
//     `hostname -f` returns the same thing.
//
// Returns the bare hostname if all strategies fail.
func resolveFQDN(host string) string {
	if strings.Contains(host, ".") {
		return host
	}
	if addrs, err := net.LookupHost(host); err == nil {
		for _, a := range addrs {
			names, err := net.LookupAddr(a)
			if err != nil {
				continue
			}
			for _, n := range names {
				n = strings.TrimSuffix(n, ".")
				if strings.HasPrefix(n, host+".") {
					return n
				}
			}
		}
	}
	if dom := firstSearchDomain(); dom != "" {
		return host + "." + dom
	}
	return host
}

// firstSearchDomain reads the first `search` or `domain` entry from
// /etc/resolv.conf. Returns "" on any read error or absence.
func firstSearchDomain() string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "search ") || strings.HasPrefix(line, "domain ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}

// readMemTotal parses /proc/meminfo's "MemTotal" line. Returns bytes
// (the file reports kB). 0 on any read/parse failure — the FE
// renders "?" rather than a nonsense number.
func readMemTotal() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// externalInterfaces walks net.Interfaces, filters out the noisy
// container/virt ones, and returns one IfaceIPs per real-looking
// interface that has at least one displayable address. Within each
// interface, IPv4 is alphasorted; IPv6 is collapsed to one address
// per /64 (preferring the stable EUI-64 form).
//
// Interface order is alphabetical for stability across reloads.
// Empty interfaces (no surviving addresses) are dropped — the banner
// would otherwise show a label with nothing after the colon.
func externalInterfaces() []IfaceIPs {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var groups []IfaceIPs
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if skipInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var v4 []string
		var v6 []net.IP
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			if v4ip := ip.To4(); v4ip != nil {
				v4 = append(v4, v4ip.String())
			} else {
				v6 = append(v6, ip)
			}
		}
		dedupSort(&v4)
		v6str := dedupV6ByPrefix(v6)
		dedupSort(&v6str)
		ips := append(v4, v6str...)
		if len(ips) == 0 {
			continue
		}
		groups = append(groups, IfaceIPs{Name: iface.Name, IPs: ips})
	}
	// Stable order: alphabetical by interface name. Stable across
	// reloads so the banner doesn't shuffle on every fetch.
	for i := 1; i < len(groups); i++ {
		for j := i; j > 0 && groups[j-1].Name > groups[j].Name; j-- {
			groups[j-1], groups[j] = groups[j], groups[j-1]
		}
	}
	return groups
}

// dedupV6ByPrefix groups IPv6 addresses by their /64 prefix and
// returns one string per group. Within a group, prefer the EUI-64
// stable address — identifiable by the kernel-injected ff:fe at
// bytes 11-12 of the 16-byte representation (RFC 4291 §2.5.1).
// When no EUI-64 address is present, the input-order-first survives.
//
// This collapses SLAAC privacy-extension noise: temporary addresses
// rotate every few hours and the kernel keeps deprecated ones around
// for incoming-only use, producing 3-4 globals per /64 in steady
// state. The banner shows the one that doesn't rotate.
func dedupV6ByPrefix(ips []net.IP) []string {
	if len(ips) == 0 {
		return nil
	}
	type slot struct {
		chosen net.IP
		stable bool // true once we've picked an EUI-64 address for this prefix
	}
	// 8-byte prefix → chosen address. map iteration is non-
	// deterministic; the outer dedupSort sorts the final strings.
	byPrefix := make(map[[8]byte]*slot)
	for _, ip := range ips {
		ip16 := ip.To16()
		if ip16 == nil {
			continue
		}
		var key [8]byte
		copy(key[:], ip16[:8])
		s, ok := byPrefix[key]
		if !ok {
			byPrefix[key] = &slot{chosen: ip, stable: isEUI64(ip16)}
			continue
		}
		if s.stable {
			// Already have the stable address; ignore everything
			// else under this prefix.
			continue
		}
		if isEUI64(ip16) {
			s.chosen = ip
			s.stable = true
		}
		// Otherwise keep the first one we saw.
	}
	out := make([]string, 0, len(byPrefix))
	for _, s := range byPrefix {
		out = append(out, s.chosen.String())
	}
	return out
}

// isEUI64 reports whether ip's lower 64 bits look like a MAC-derived
// EUI-64 interface identifier. The kernel inserts 0xFFFE at bytes
// 11-12 when expanding a 48-bit MAC into a 64-bit IID, and that
// byte pair is essentially never produced by RFC 4941 temporary
// addresses (which roll 64 random bits). False positive rate is
// 1 in 2^16 for purely random addresses, acceptable for "pick the
// nicer one to display."
//
// ip must be the 16-byte representation (call To16 first).
func isEUI64(ip16 net.IP) bool {
	if len(ip16) != 16 {
		return false
	}
	return ip16[11] == 0xFF && ip16[12] == 0xFE
}

// skipInterface reports whether name looks like a container /
// virtualization-only interface that the user wouldn't think of as
// "their box's address." Conservative — anything not matched here
// is kept.
//
// Note: `br-` is NOT in the prefix list because user-named bridges
// (br-lan, br-wan, br-kids, …) are legitimate primary interfaces on
// router-style boxes. Docker auto-creates `br-<12-hex>` names, which
// the dockerBridge check below filters specifically.
func skipInterface(name string) bool {
	prefixes := []string{
		"docker", "veth", "virbr", "vmnet", "tun", "tap",
		"wg", "tailscale", "zt", "cilium", "kube", "flannel", "cni",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return isDockerBridge(name)
}

// isDockerBridge matches Docker's auto-generated bridge names like
// br-1a2b3c4d5e6f. Format is "br-" + 12 hex chars. Bridges named
// otherwise (br-lan, br-wan, br0) survive.
func isDockerBridge(name string) bool {
	const prefix = "br-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+12 {
		return false
	}
	for _, r := range name[len(prefix):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// dedupSort sorts ss in place and removes adjacent duplicates.
// Helper for externalInterfaces, which can see the same address on
// multiple interfaces in some VPN configurations.
func dedupSort(ss *[]string) {
	if len(*ss) < 2 {
		return
	}
	// Tiny slices — insertion sort + two-pointer dedupe is fine;
	// avoids pulling in sort just for this.
	s := *ss
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
	// Two-pointer dedup. w is the write cursor pointing at the last
	// unique element; advance it only when the next read produces a
	// new value. s[0] is always unique by definition (the first
	// element of any non-empty sequence).
	w := 0
	for i := 1; i < len(s); i++ {
		if s[i] != s[w] {
			w++
			s[w] = s[i]
		}
	}
	*ss = s[:w+1]
}

// sysInfoString is a debug-print helper for logs; FE renders the
// fields itself.
func sysInfoString(s sysInfo) string {
	parts := make([]string, 0, len(s.Interfaces))
	for _, g := range s.Interfaces {
		parts = append(parts, g.Name+"="+strings.Join(g.IPs, ","))
	}
	router := "v" + s.Router.Version
	if s.Router.Commit != "" {
		router += "/" + s.Router.Commit
	}
	if s.Router.Dev {
		router += "/dev"
	}
	return fmt.Sprintf("host=%s fqdn=%s user=%s cpus=%d mem=%dMB router=%s ifaces=[%s]",
		s.Hostname, s.FQDN, s.Username, s.CPUs, s.MemBytes/(1024*1024), router,
		strings.Join(parts, " "))
}
