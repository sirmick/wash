package nm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/nmprofile"
)

// Capabilities is the type-2 (workstation) profile NM covers (docs/NET.md §5):
// interfaces (incl. VLAN/bridge), routes, wifi-client, WireGuard. Firewall
// zones, the DHCP server, and AP mode are NOT NM's job, so they stay out — the
// UI greys them and validation flags any config that reaches for them.
func Capabilities() caps.Capabilities {
	return caps.New([]string{
		"network/interface",
		"network/device",
		"network/route",
		"network/rule",
		"network/wireguard_peer",
		"wireless/wifi-iface",
		"wireless/wifi-device",
	}, caps.CanWireGuard, caps.CanVLAN, caps.CanBridge, caps.CanPolicyRouting)
}

// Applier is the backend.Applier over live NetworkManager. Apply snapshots the
// keyfile directory before writing so commit-confirm can Rollback to it; Confirm
// drops the snapshot. dir is overridable for tests.
type Applier struct {
	dir string

	mu           sync.Mutex
	seq          int
	snapshots    map[backend.RollbackToken]map[string]string // dir contents at Apply time
	lastHadRoute bool                                        // default route present just before the last Apply
}

// NewApplier builds an NM-backed Applier writing to NM's system-connections dir.
func NewApplier() *Applier {
	return &Applier{dir: SystemConnDir, snapshots: map[backend.RollbackToken]map[string]string{}}
}

func (a *Applier) Capabilities() caps.Capabilities { return Capabilities() }

func (a *Applier) Render(c model.Config) (backend.Artifacts, error) {
	kfs, err := nmprofile.RenderKeyfiles(c)
	if err != nil {
		return nil, err
	}
	out := make(backend.Artifacts, len(kfs))
	for id, text := range kfs {
		out[id+".nmconnection"] = text
	}
	return out, nil
}

// Apply snapshots the current keyfiles, then installs + activates the target's.
func (a *Applier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	a.mu.Lock()
	snap, err := snapshotDir(a.dir)
	if err != nil {
		a.mu.Unlock()
		return "", err
	}
	a.seq++
	token := backend.RollbackToken("nm-" + strconv.Itoa(a.seq))
	a.snapshots[token] = snap
	a.mu.Unlock()

	c, err := Connect()
	if err != nil {
		return token, err
	}
	defer c.Close()
	// Baseline whether the box has a default route BEFORE the change, so Verify
	// can spot the lock-out: an apply that severs the box's own way out. (NM's
	// own connectivity check is unreliable here — Alpine ships no check URI, so
	// it just reports "full" regardless — but the kernel routing table is ground
	// truth.)
	a.mu.Lock()
	a.lastHadRoute = HasDefaultRoute()
	a.mu.Unlock()
	if _, err := c.applyTo(a.dir, p.Target); err != nil {
		return token, err
	}
	return token, nil
}

// Verify is the commit-confirm health check (docs/NET.md §7, §2.9). The lock-out
// trigger: if the box had a default route before the apply and lost it after,
// the change cut its way out — fail so the engine auto-reverts. When there was
// no route to lose it just checks NM is reachable and not wedged. Routes settle
// asynchronously (NM tears the old connection's route down a beat after the new
// one activates), so allow a settle + poll before declaring a regression.
func (a *Applier) Verify(model.Config) error {
	c, err := Connect()
	if err != nil {
		return fmt.Errorf("verify: NM unreachable: %w", err)
	}
	defer c.Close()

	a.mu.Lock()
	hadRoute := a.lastHadRoute
	a.mu.Unlock()

	if !hadRoute {
		s, err := c.Status()
		if err != nil {
			return fmt.Errorf("verify: NM status: %w", err)
		}
		if s.State != 0 && s.State < 40 { // asleep/disconnecting/disconnected
			return fmt.Errorf("verify: NM state %s after apply", StateName(s.State))
		}
		return nil
	}

	time.Sleep(5 * time.Second) // let the route reconcile
	for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
		if HasDefaultRoute() {
			return nil // route held (or was replaced) — the apply is fine
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("verify: lost the default route after apply — reverting (lock-out protection)")
}

// HasDefaultRoute reports whether the kernel has any IPv4 default route
// (destination 0.0.0.0), read straight from /proc/net/route.
func HasDefaultRoute() bool {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	for _, line := range lines[1:] { // skip header
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return true
		}
	}
	return false
}

func (a *Applier) Confirm(token backend.RollbackToken) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.snapshots, token) // keep the applied keyfiles; drop the undo snapshot
	return nil
}

// Rollback restores the keyfiles captured at Apply time and reloads NM.
func (a *Applier) Rollback(token backend.RollbackToken) error {
	a.mu.Lock()
	snap, ok := a.snapshots[token]
	delete(a.snapshots, token)
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("rollback: unknown token %q", token)
	}
	if err := restoreDir(a.dir, snap); err != nil {
		return err
	}
	c, err := Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.ReloadConnections(); err != nil {
		return err
	}
	// Reload updates the profiles but doesn't re-apply them to the devices, so
	// the box would still be running the failed config. Reactivate each restored
	// connection (Activate deactivates the stale live instance first) so the
	// revert actually takes — the lock-out's whole point.
	for name := range snap {
		_ = c.Activate(strings.TrimSuffix(name, ".nmconnection"))
	}
	return nil
}

// Live reads the box's current networking — the base netd diffs an edit against.
func (a *Applier) Live() model.Config {
	cfg, _ := readDir(a.dir)
	return cfg
}

// snapshotDir captures every *.nmconnection file's contents in dir.
func snapshotDir(dir string) (map[string]string, error) {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".nmconnection") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// restoreDir makes dir's *.nmconnection files exactly match snap (writing the
// captured ones, removing any that were added since).
func restoreDir(dir string, snap map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cur, err := snapshotDir(dir)
	if err != nil {
		return err
	}
	for name := range cur {
		if _, keep := snap[name]; !keep {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	for name, text := range snap {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Devices lists the managed link names NM sees (eth0, …), so the FE wizards can
// offer them as VLAN parents / bridge members even before they carry a config.
// Loopback and already-virtual links (vlan/bridge) are filtered out.
func (a *Applier) Devices() []string {
	c, err := Connect()
	if err != nil {
		return nil
	}
	defer c.Close()
	s, err := c.Status()
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range s.Devices {
		if d.Type == 1 && d.Interface != "" { // NM_DEVICE_TYPE_ETHERNET
			out = append(out, d.Interface)
		}
	}
	return out
}
