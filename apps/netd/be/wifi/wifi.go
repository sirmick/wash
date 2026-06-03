// Package wifi is netd's wifi runtime: a thin wrapper over nmcli (CLI only, no
// D-Bus). It sits BESIDE the config backend — it does NOT go through the
// netplan-takeover/txn engine. NetworkManager owns wifi connections
// imperatively (the GNOME-applet path: keyfiles in
// /etc/NetworkManager/system-connections), so a connect never enters the apply
// document; it's a direct side-channel. Detect/Scan/Status are unprivileged
// reads; Connect/Forget mutate NM and need polkit, so netd drives them through
// cmd/washnet-wifi under the wash-priv escalation.
//
// The exec seam (runner) is injectable for hermetic tests, mirroring the
// netplan backend's runner.
package wifi

import (
	"context"
	"strings"
)

// AP is one scanned access point — a row of `nmcli device wifi list`.
type AP struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`   // 0-100
	Security string `json:"security"` // "" = open, else "WPA2", "WPA3", "WPA2 WPA3", ...
	InUse    bool   `json:"in_use"`   // currently the active connection
}

// WifiConn is an active wifi connection — a row of `nmcli connection show --active`.
type WifiConn struct {
	Name   string `json:"name"`
	Device string `json:"device"`
}

// Security tags accepted by Connect — they mirror model.Encryption.UCITag().
const (
	SecNone = "none"
	SecPSK2 = "psk2"
	SecSAE  = "sae"
)

// WifiRuntime is the nmcli-backed wifi surface. Detect/Scan/Status are
// unprivileged reads; Connect/Forget mutate NM and need root.
type WifiRuntime interface {
	// Detect reports whether a wifi radio is present and whether NM is the live
	// manager. It never errors — absence of nmcli or NM ⇒ (false, false), so the
	// FE simply hides the wifi UI.
	Detect() (radioPresent, nmLive bool)
	// Enabled reports NM's software wifi switch (a radio can be present but
	// switched off — then scanning is refused). SetEnabled flips it; it needs
	// polkit, so it's driven through the escalation like Connect/Forget.
	Enabled(ctx context.Context) bool
	SetEnabled(ctx context.Context, on bool) (string, error)
	Scan(ctx context.Context) ([]AP, error)
	Status(ctx context.Context) ([]WifiConn, error)
	Connect(ctx context.Context, ssid, security, psk string, hidden bool) (string, error)
	Forget(ctx context.Context, ssid string) (string, error)
}

// Live is the real nmcli-backed runtime. radio is the radio-presence probe,
// injectable for tests; it defaults to a sysfs scan so radio detection is
// INDEPENDENT of NetworkManager (a no-NM netplan box still reports its radio,
// which the FE needs to show advanced/manual mode there).
type Live struct {
	run   runner
	radio func() []string
}

// New builds a Live over the real exec runner and the sysfs radio probe.
func New() *Live { return &Live{run: execRunner{}, radio: RadioDevices} }

var _ WifiRuntime = (*Live)(nil)

// Detect reports radio presence (sysfs, NM-independent) and whether NM is the
// live manager (`nmcli general status` succeeds). The two are deliberately
// orthogonal: a box can have a radio without NM (declarative/advanced only), or
// NM without our knowing the radio yet.
func (l *Live) Detect() (radioPresent, nmLive bool) {
	probe := l.radio
	if probe == nil {
		probe = RadioDevices
	}
	radioPresent = len(probe()) > 0
	out, err := l.run.runStdout(context.Background(), "nmcli", "-t", "-f", "STATE", "general", "status")
	nmLive = err == nil && strings.TrimSpace(out) != ""
	return radioPresent, nmLive
}

// Enabled reads NM's software wifi switch (WIFI column of `general status`).
func (l *Live) Enabled(ctx context.Context) bool {
	out, err := l.run.runStdout(ctx, "nmcli", "-t", "-f", "WIFI", "general", "status")
	return err == nil && strings.TrimSpace(out) == "enabled"
}

// SetEnabled flips NM's software wifi switch (privileged: polkit).
func (l *Live) SetEnabled(ctx context.Context, on bool) (string, error) {
	state := "off"
	if on {
		state = "on"
	}
	return l.run.run(ctx, "nmcli", "radio", "wifi", state)
}

// Scan rescans (best-effort — NM rate-limits it on fast polls) then lists. The
// rescan error is intentionally ignored: on a 2-3s poll NM replies "Scanning
// not allowed immediately following previous scan", and we just want whatever
// it already has cached.
func (l *Live) Scan(ctx context.Context) ([]AP, error) {
	_, _ = l.run.run(ctx, "nmcli", "device", "wifi", "rescan")
	out, err := l.run.runStdout(ctx, "nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "device", "wifi", "list")
	if err != nil {
		return nil, err
	}
	return parseScan(out), nil
}

// Status lists active wifi connections (802-11-wireless only).
func (l *Live) Status(ctx context.Context) ([]WifiConn, error) {
	out, err := l.run.runStdout(ctx, "nmcli", "-t", "-f", "NAME,TYPE,DEVICE", "connection", "show", "--active")
	if err != nil {
		return nil, err
	}
	return parseActiveWifi(out), nil
}

// Connect provisions a profile named after the SSID and brings it up. We set the
// security EXPLICITLY (key-mgmt + psk) rather than letting `nmcli device wifi
// connect` infer it from the scan cache — that inference fails with "key-mgmt is
// missing" whenever the AP isn't in NM's current scan results, and being
// explicit also lets a not-in-range network be pre-configured (the expert/manual
// path). The add is made idempotent by first dropping any stale same-named
// profile. Returns nmcli's combined output for diagnostics.
func (l *Live) Connect(ctx context.Context, ssid, security, psk string, hidden bool) (string, error) {
	_, _ = l.run.run(ctx, "nmcli", "connection", "delete", "id", ssid) // ignore "no such connection"
	args := []string{"connection", "add", "type", "wifi", "con-name", ssid, "ssid", ssid}
	switch security {
	case SecPSK2:
		args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", psk)
	case SecSAE:
		args = append(args, "wifi-sec.key-mgmt", "sae", "wifi-sec.psk", psk)
	}
	if hidden {
		args = append(args, "802-11-wireless.hidden", "yes")
	}
	if out, err := l.run.run(ctx, "nmcli", args...); err != nil {
		return out, err
	}
	// Bound activation so a network with no DHCP server returns a clean timeout
	// instead of blocking until the process is killed.
	return l.run.run(ctx, "nmcli", "--wait", "25", "connection", "up", ssid)
}

// Forget deletes the NM connection profile named ssid.
func (l *Live) Forget(ctx context.Context, ssid string) (string, error) {
	return l.run.run(ctx, "nmcli", "connection", "delete", "id", ssid)
}
