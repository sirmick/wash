// Package nm is wash-net's NetworkManager backend (docs/NET.md §5): the type-2
// (workstation) renderer, driven pure-Go over D-Bus via godbus — no libnm, no
// cgo. B4b stands up the connection + a read-only status probe; the Applier
// (connection add/update/activate + reconcile) lands in B4c.
package nm

import (
	"fmt"

	"github.com/godbus/dbus/v5"

	"github.com/sirmick/wash/internal/washnet/backend"
)

// Detect probes whether NetworkManager is reachable on D-Bus and actively
// managing (docs/NET.md §2.7) — read-only, for netd's `auto` selection. A
// reachable, non-asleep NM means the box is NM-managed: the coexist-safe choice.
func Detect() backend.Detection {
	c, err := Connect()
	if err != nil {
		return backend.Detection{Note: "NetworkManager not reachable on D-Bus"}
	}
	defer c.Close()
	s, err := c.Status()
	if err != nil {
		return backend.Detection{Available: true, Note: "NetworkManager present but not answering"}
	}
	if s.State == 10 { // NM_STATE_ASLEEP
		return backend.Detection{Available: true, Note: "NetworkManager asleep"}
	}
	return backend.Detection{Available: true, Active: true, Note: "NetworkManager active"}
}

const (
	dest     = "org.freedesktop.NetworkManager"
	objPath  = "/org/freedesktop/NetworkManager"
	iface    = "org.freedesktop.NetworkManager"
	devIface = "org.freedesktop.NetworkManager.Device"
)

// Conn is a system-bus connection bound to the NetworkManager object.
type Conn struct {
	bus *dbus.Conn
	nm  dbus.BusObject
}

// Connect dials the system bus and binds NetworkManager. The caller must Close.
func Connect() (*Conn, error) {
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("nm: system bus: %w", err)
	}
	return &Conn{bus: bus, nm: bus.Object(dest, objPath)}, nil
}

// Close releases the bus connection.
func (c *Conn) Close() error { return c.bus.Close() }

// Device is one NM-managed link.
type Device struct {
	Path      string `json:"path"`
	Interface string `json:"interface"`
	Type      uint32 `json:"type"`  // NMDeviceType
	State     uint32 `json:"state"` // NMDeviceState
}

// Status is a snapshot of NM's top-level state plus its devices.
type Status struct {
	Version      string   `json:"version"`
	State        uint32   `json:"state"`        // NMState
	Connectivity uint32   `json:"connectivity"` // NMConnectivityState
	Devices      []Device `json:"devices"`
}

func str(v dbus.Variant) string { s, _ := v.Value().(string); return s }
func u32(v dbus.Variant) uint32 { n, _ := v.Value().(uint32); return n }

// Status reads NM's version, overall state, connectivity, and device list.
func (c *Conn) Status() (*Status, error) {
	s := &Status{}
	if v, err := c.nm.GetProperty(iface + ".Version"); err == nil {
		s.Version = str(v)
	}
	if v, err := c.nm.GetProperty(iface + ".State"); err == nil {
		s.State = u32(v)
	}
	if v, err := c.nm.GetProperty(iface + ".Connectivity"); err == nil {
		s.Connectivity = u32(v)
	}
	var paths []dbus.ObjectPath
	if err := c.nm.Call(iface+".GetDevices", 0).Store(&paths); err != nil {
		return s, fmt.Errorf("nm: GetDevices: %w", err)
	}
	for _, p := range paths {
		o := c.bus.Object(dest, p)
		d := Device{Path: string(p)}
		if v, err := o.GetProperty(devIface + ".Interface"); err == nil {
			d.Interface = str(v)
		}
		if v, err := o.GetProperty(devIface + ".DeviceType"); err == nil {
			d.Type = u32(v)
		}
		if v, err := o.GetProperty(devIface + ".State"); err == nil {
			d.State = u32(v)
		}
		s.Devices = append(s.Devices, d)
	}
	return s, nil
}

// Connectivity levels (NMConnectivityState).
const (
	ConnUnknown uint32 = 0
	ConnNone    uint32 = 1
	ConnPortal  uint32 = 2
	ConnLimited uint32 = 3
	ConnFull    uint32 = 4
)

// CheckConnectivity forces an immediate connectivity check and returns the
// resulting NMConnectivityState. Unlike the cached Connectivity property this is
// synchronous, which is what makes the commit-confirm VERIFY deterministic.
func (c *Conn) CheckConnectivity() (uint32, error) {
	var state uint32
	err := c.nm.Call(iface+".CheckConnectivity", 0).Store(&state)
	return state, err
}

// StateName renders an NMState enum value (nm-dbus-types.h).
func StateName(s uint32) string {
	switch s {
	case 10:
		return "asleep"
	case 20:
		return "disconnected"
	case 30:
		return "disconnecting"
	case 40:
		return "connecting"
	case 50:
		return "connected-local"
	case 60:
		return "connected-site"
	case 70:
		return "connected-global"
	}
	return "unknown"
}

// ConnectivityName renders an NMConnectivityState enum value.
func ConnectivityName(c uint32) string {
	switch c {
	case 1:
		return "none"
	case 2:
		return "portal"
	case 3:
		return "limited"
	case 4:
		return "full"
	}
	return "unknown"
}

// DeviceTypeName renders the common NMDeviceType values.
func DeviceTypeName(t uint32) string {
	switch t {
	case 1:
		return "ethernet"
	case 2:
		return "wifi"
	case 5:
		return "bluetooth"
	case 13:
		return "bridge"
	case 11:
		return "vlan"
	case 14:
		return "generic"
	case 16:
		return "tun"
	case 29:
		return "wireguard"
	case 32:
		return "loopback"
	}
	return fmt.Sprintf("type-%d", t)
}
