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
	Scan(ctx context.Context) ([]AP, error)
	Status(ctx context.Context) ([]WifiConn, error)
	Connect(ctx context.Context, ssid, security, psk string, hidden bool) (string, error)
	Forget(ctx context.Context, ssid string) (string, error)
}

// Live is the real nmcli-backed runtime.
type Live struct{ run runner }

// New builds a Live over the real exec runner.
func New() *Live { return &Live{run: execRunner{}} }

var _ WifiRuntime = (*Live)(nil)

// Detect runs one `nmcli general status`: success ⇒ NM is live; WIFI-HW=enabled
// ⇒ a radio is present. nmcli absent or NM not running ⇒ both false.
func (l *Live) Detect() (radioPresent, nmLive bool) {
	out, err := l.run.runStdout(context.Background(), "nmcli", "-t", "-f", "STATE,WIFI-HW,WIFI", "general", "status")
	if err != nil {
		return false, false
	}
	f := splitTerse(strings.TrimSpace(out))
	nmLive = len(f) >= 1 && f[0] != ""
	radioPresent = len(f) >= 2 && f[1] == "enabled" // WIFI-HW column
	return radioPresent, nmLive
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

// Connect associates with ssid. Security selects whether a password is passed
// (open ⇒ none); nmcli infers WPA2-vs-WPA3 from the AP / negotiation. hidden
// adds `hidden yes` for non-broadcast SSIDs (the expert/manual path). Returns
// nmcli's combined output for diagnostics.
func (l *Live) Connect(ctx context.Context, ssid, security, psk string, hidden bool) (string, error) {
	args := []string{"device", "wifi", "connect", ssid}
	if security != SecNone && psk != "" {
		args = append(args, "password", psk)
	}
	if hidden {
		args = append(args, "hidden", "yes")
	}
	return l.run.run(ctx, "nmcli", args...)
}

// Forget deletes the NM connection profile named ssid.
func (l *Live) Forget(ctx context.Context, ssid string) (string, error) {
	return l.run.run(ctx, "nmcli", "connection", "delete", "id", ssid)
}
