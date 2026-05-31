package nm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
//
// If the connection is ALREADY active (e.g. we just rewrote an interface's
// keyfile), ActivateConnection alone won't re-read the changed settings — NM
// considers it active and no-ops. So deactivate the live instance first to force
// a fresh apply. For a brand-new connection this finds nothing and is a no-op.
func (c *Conn) Activate(id string) error {
	p, err := c.connectionByID(id)
	if err != nil {
		return err
	}
	c.deactivateConnection(p)
	// ActivateConnection's polkit check can transiently come back "Authorization
	// request cancelled": just after boot polkit may still be parsing rules.d, and
	// when a bridge activates, enslaving its ports churns the device state and NM
	// cancels the in-flight auth. Both clear on a retry — so retry that specific
	// error a few times; any other error (a real config problem) returns at once.
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		var ac dbus.ObjectPath
		lastErr = c.nm.Call(iface+".ActivateConnection", 0, p, dbus.ObjectPath("/"), dbus.ObjectPath("/")).Store(&ac)
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "Authorization request cancelled") {
			return lastErr
		}
	}
	return lastErr
}

// activeConnPathFor returns the ActiveConnection object path backing the given
// settings-connection path, or "" if that connection isn't currently active.
func (c *Conn) activeConnPathFor(connPath dbus.ObjectPath) dbus.ObjectPath {
	v, err := c.nm.GetProperty(iface + ".ActiveConnections")
	if err != nil {
		return ""
	}
	actives, _ := v.Value().([]dbus.ObjectPath)
	for _, ac := range actives {
		o := c.bus.Object(dest, ac)
		cv, err := o.GetProperty(iface + ".Connection.Active.Connection")
		if err != nil {
			continue
		}
		if p, _ := cv.Value().(dbus.ObjectPath); p == connPath {
			return ac
		}
	}
	return ""
}

// deactivateConnection tears down any active instance of the given settings
// connection so its next activation re-reads changed settings. Best-effort.
func (c *Conn) deactivateConnection(connPath dbus.ObjectPath) {
	if ac := c.activeConnPathFor(connPath); ac != "" {
		_ = c.nm.Call(iface+".DeactivateConnection", 0, ac).Err
	}
}

// isActive reports whether the named connection currently has an active
// instance, so an apply can skip re-activating connections it didn't change.
func (c *Conn) isActive(id string) bool {
	p, err := c.connectionByID(id)
	if err != nil {
		return false
	}
	return c.activeConnPathFor(p) != ""
}

// Apply renders cfg to NM keyfiles, installs them in the system-connections dir
// (root-owned 0600, as NM requires), reloads NM, and activates every connection.
func (c *Conn) Apply(cfg model.Config) ([]string, error) { return c.applyTo(SystemConnDir, cfg) }

// applyTo is Apply against an explicit keyfile dir (overridable for tests).
// Masters before members so a bridge exists before its ports enslave. Returns
// the connection ids it installed.
func (c *Conn) applyTo(dir string, cfg model.Config) ([]string, error) {
	kfs, err := nmprofile.RenderKeyfiles(cfg)
	if err != nil {
		return nil, err
	}

	// Remove keyfiles wash previously wrote that are no longer in the desired
	// config — a removed connection, or a NIC that became a bridge port and so
	// lost its standalone profile. netd owns the whole keyfile dir as the model,
	// so anything not re-rendered must go, otherwise the stale profile lingers
	// and stays active. Deactivate while NM still has it loaded (before the reload
	// below drops it) so the link actually comes down.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".nmconnection") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".nmconnection")
			if _, keep := kfs[id]; keep {
				continue
			}
			if p, err := c.connectionByID(id); err == nil {
				c.deactivateConnection(p)
			}
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}

	ids := make([]string, 0, len(kfs))
	changed := make(map[string]bool, len(kfs))
	for id, text := range kfs {
		ids = append(ids, id)
		p := filepath.Join(dir, id+".nmconnection")
		// Track whether this keyfile actually differs from what's on disk. An
		// apply that only adds a vlan leaves the bridge's keyfile byte-identical;
		// re-activating it would needlessly bounce the bridge — and bouncing an
		// active connection mid-apply makes NM cancel the in-flight polkit auth
		// ("Authorization request cancelled"). So only touch what changed.
		if old, err := os.ReadFile(p); err != nil || string(old) != text {
			changed[id] = true
		}
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
		// Leave unchanged, already-up connections alone — reload already re-read
		// them, and a deactivate+reactivate cycle here is what trips NM's auth.
		if !changed[id] && c.isActive(id) {
			continue
		}
		if err := c.Activate(id); err != nil {
			return ids, fmt.Errorf("activate %s: %w", id, err)
		}
	}
	return ids, nil
}
