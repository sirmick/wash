package wifi

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RadioDevices returns the netdevs that are 802.11 radios — those with a
// `wireless/` subdir under /sys/class/net. It reads sysfs directly (no nmcli,
// no root), so it works on no-NM boxes too; the FE uses the first name as the
// device for the declarative (netplan) advanced path.
func RadioDevices() []string {
	matches, _ := filepath.Glob("/sys/class/net/*/wireless")
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, filepath.Base(filepath.Dir(m))) // /sys/class/net/<dev>/wireless
	}
	sort.Strings(out)
	return out
}

// splitTerse splits one `nmcli -t` line on its unescaped ':' separators,
// unescaping each field. nmcli -t escapes ':' as `\:` and '\' as `\\` inside
// values, so a naive strings.Split(line, ":") corrupts an SSID like "foo:bar"
// — the classic nmcli terse-mode footgun.
func splitTerse(line string) []string {
	var fields []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			b.WriteByte(line[i+1]) // escaped char taken literally
			i++
		case c == ':':
			fields = append(fields, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	fields = append(fields, b.String())
	return fields
}

// parseScan parses `nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY device wifi list`.
// Empty-SSID rows (hidden networks) are dropped — they can't be joined by name
// in the picker. Duplicate SSIDs (one per BSSID) collapse to the strongest
// signal, preserving an in-use marking. Order is first-seen.
func parseScan(out string) []AP {
	best := map[string]AP{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f := splitTerse(line)
		if len(f) < 4 {
			continue
		}
		ssid := f[1]
		if ssid == "" {
			continue // hidden / not broadcasting an SSID
		}
		sig, _ := strconv.Atoi(f[2])
		ap := AP{SSID: ssid, Signal: sig, Security: normalizeSecurity(f[3]), InUse: f[0] == "*"}
		if prev, ok := best[ssid]; ok {
			if ap.Signal > prev.Signal {
				ap.InUse = ap.InUse || prev.InUse
				best[ssid] = ap
			} else if ap.InUse && !prev.InUse {
				prev.InUse = true
				best[ssid] = prev
			}
			continue
		}
		best[ssid] = ap
		order = append(order, ssid)
	}
	aps := make([]AP, 0, len(order))
	for _, s := range order {
		aps = append(aps, best[s])
	}
	return aps
}

// normalizeSecurity maps nmcli's SECURITY column to a token: "--" or "" mean an
// open network, anything else passes through ("WPA2", "WPA3", "WPA2 WPA3", ...).
func normalizeSecurity(s string) string {
	if s = strings.TrimSpace(s); s == "" || s == "--" {
		return ""
	}
	return s
}

// parseActiveWifi parses `nmcli -t -f NAME,TYPE,DEVICE connection show --active`,
// keeping only 802-11-wireless rows.
func parseActiveWifi(out string) []WifiConn {
	var conns []WifiConn
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f := splitTerse(line)
		if len(f) < 3 || f[1] != "802-11-wireless" {
			continue
		}
		conns = append(conns, WifiConn{Name: f[0], Device: f[2]})
	}
	return conns
}
