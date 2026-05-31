package nm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/nmprofile"
)

// SystemConnDir is where NM's keyfile plugin reads saved connections.
const SystemConnDir = "/etc/NetworkManager/system-connections"

func (c *Conn) settings() dbus.BusObject {
	return c.bus.Object(dest, "/org/freedesktop/NetworkManager/Settings")
}

// ReloadConnections makes NM re-read the keyfile directory (after we write).
func (c *Conn) ReloadConnections() error {
	var ok bool
	return c.settings().Call(iface+".Settings.ReloadConnections", 0).Store(&ok)
}

// connectionByID resolves a saved connection's object path by its connection.id.
func (c *Conn) connectionByID(id string) (dbus.ObjectPath, error) {
	var paths []dbus.ObjectPath
	if err := c.settings().Call(iface+".Settings.ListConnections", 0).Store(&paths); err != nil {
		return "", err
	}
	for _, p := range paths {
		var s map[string]map[string]dbus.Variant
		if err := c.bus.Object(dest, p).Call(iface+".Settings.Connection.GetSettings", 0).Store(&s); err != nil {
			continue
		}
		if cn := s["connection"]; cn != nil {
			if got, _ := cn["id"].Value().(string); got == id {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("connection %q not found", id)
}

// Activate brings a connection up, letting NM pick/create the device (device
// and specific-object "/"). Activating a bridge-port pulls up its master; a
// vlan/bridge connection creates its link.
func (c *Conn) Activate(id string) error {
	p, err := c.connectionByID(id)
	if err != nil {
		return err
	}
	var ac dbus.ObjectPath
	return c.nm.Call(iface+".ActivateConnection", 0, p, dbus.ObjectPath("/"), dbus.ObjectPath("/")).Store(&ac)
}

// Apply renders cfg to NM keyfiles, installs them in the system-connections dir
// (root-owned 0600, as NM requires), reloads NM, and activates every connection.
// Masters before members so a bridge exists before its ports enslave. Returns
// the connection ids it installed.
func (c *Conn) Apply(cfg model.Config) ([]string, error) {
	kfs, err := nmprofile.RenderKeyfiles(cfg)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(kfs))
	for id, text := range kfs {
		ids = append(ids, id)
		p := filepath.Join(SystemConnDir, id+".nmconnection")
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			return ids, fmt.Errorf("write %s: %w", p, err)
		}
	}
	if err := c.ReloadConnections(); err != nil {
		return ids, fmt.Errorf("reload: %w", err)
	}

	// Activate masters (non-enslaved connections) before members, so the bridge
	// exists when its ports enslave.
	isPort := func(id string) bool { return strings.Contains(kfs[id], "slave-type=") }
	sort.SliceStable(ids, func(a, b int) bool { return !isPort(ids[a]) && isPort(ids[b]) })
	for _, id := range ids {
		if err := c.Activate(id); err != nil {
			return ids, fmt.Errorf("activate %s: %w", id, err)
		}
	}
	return ids, nil
}
