package networkd

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/networkdprofile"
)

// Applier is the backend.Applier over live systemd-networkd. Apply snapshots the
// unit directory before writing so commit-confirm can Rollback to it; Confirm
// drops the snapshot. dir, run, and route are overridable for tests.
type Applier struct {
	dir   string
	run   runner
	route func() bool // default-route probe (injectable; defaults to /proc/net/route)

	// commit-confirm lock-out poll window (§7); fields so tests run fast.
	settle   time.Duration // grace before sampling, for networkd to begin reconciling
	window   time.Duration // how long the route may stay gone before declaring lock-out
	interval time.Duration // sample period

	mu           sync.Mutex
	seq          int
	snapshots    map[backend.RollbackToken]map[string]string
	lastHadRoute bool
}

// NewApplier builds a networkd-backed Applier writing to /etc/systemd/network.
func NewApplier() *Applier {
	return &Applier{
		dir:       UnitDir,
		run:       execRunner{},
		route:     hasDefaultRoute,
		settle:    time.Second,
		window:    14 * time.Second,
		interval:  500 * time.Millisecond,
		snapshots: map[backend.RollbackToken]map[string]string{},
	}
}

func (a *Applier) Capabilities() caps.Capabilities { return Capabilities() }

func (a *Applier) Render(c model.Config) (backend.Artifacts, error) {
	units, err := networkdprofile.RenderUnits(c)
	if err != nil {
		return nil, err
	}
	return backend.Artifacts(units), nil // already filename→text, with extensions
}

// Apply snapshots the current units, installs the target's, then reloads
// networkd and reconfigures the affected links.
func (a *Applier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	units, err := networkdprofile.RenderUnits(p.Target)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	snap, err := snapshotDir(a.dir)
	if err != nil {
		a.mu.Unlock()
		return "", err
	}
	a.seq++
	token := backend.RollbackToken("networkd-" + strconv.Itoa(a.seq))
	a.snapshots[token] = snap
	// Baseline whether the box has a default route BEFORE the change so Verify can
	// detect the lock-out (an apply that severs the box's own way out).
	a.lastHadRoute = a.route()
	a.mu.Unlock()

	// The applier can take the box offline; every step it takes must
	// leave a trail (unit set, reload, per-link reconfigure results).
	log.Printf("networkd: apply token=%s dir=%s units=%v", token, a.dir, unitNames(units))
	if err := writeDir(a.dir, units); err != nil {
		log.Printf("networkd: apply token=%s write units: %v", token, err)
		return token, err
	}
	if _, err := a.run.run("networkctl", "reload"); err != nil {
		log.Printf("networkd: apply token=%s: %v", token, err)
		return token, err
	}
	// reconfigure re-applies the reloaded config to each touched link. Best-effort
	// per link: a link that isn't present yet (e.g. a vlan being created) errors
	// harmlessly, and the reload already armed it for when it appears — but
	// best-effort must not mean silent, so failures are logged.
	for _, link := range affectedLinks(p.Target) {
		if _, err := a.run.run("networkctl", "reconfigure", link); err != nil {
			log.Printf("networkd: apply token=%s reconfigure %s (best-effort): %v", token, link, err)
		}
	}
	return token, nil
}

// unitNames returns the sorted filenames of a unit set, for log lines.
func unitNames(units map[string]string) []string {
	names := make([]string, 0, len(units))
	for n := range units {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Verify is the commit-confirm health check (docs/NET.md §7, §2.9): the lock-out
// trigger is losing the default route the box had before the apply. Mirrors the
// NM backend's logic — networkd reconfigures asynchronously, so a route may drop
// and return, hence the settle + stable-streak poll rather than a single sample.
func (a *Applier) Verify(model.Config) error {
	if _, err := a.run.run("networkctl", "status"); err != nil {
		return fmt.Errorf("verify: networkd unreachable after apply: %w", err)
	}

	a.mu.Lock()
	hadRoute := a.lastHadRoute
	a.mu.Unlock()
	if !hadRoute {
		// No default route to lose ⇒ no lock-out risk; a disconnected box is a
		// valid result (an interface set to none/no-IP, or awaiting carrier), so
		// don't require connectivity — networkd answering is enough.
		log.Printf("networkd: verify: no default route before apply, skipping lock-out poll")
		return nil
	}

	log.Printf("networkd: verify: had default route before apply, polling window=%s", a.window)
	time.Sleep(a.settle)
	const need = 3 // consecutive present samples ≈ 1.5s of stability
	streak := 0
	for deadline := time.Now().Add(a.window); time.Now().Before(deadline); {
		if a.route() {
			if streak++; streak >= need {
				return nil
			}
		} else {
			streak = 0
		}
		time.Sleep(a.interval)
	}
	return fmt.Errorf("verify: lost the default route after apply — reverting (lock-out protection)")
}

func (a *Applier) Confirm(token backend.RollbackToken) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.snapshots, token) // keep the applied units; drop the undo snapshot
	return nil
}

// Rollback restores the units captured at Apply time and reloads networkd.
func (a *Applier) Rollback(token backend.RollbackToken) error {
	a.mu.Lock()
	snap, ok := a.snapshots[token]
	delete(a.snapshots, token)
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("rollback: unknown token %q", token)
	}
	log.Printf("networkd: rollback token=%s restoring units=%v", token, unitNames(snap))
	if err := restoreDir(a.dir, snap); err != nil {
		log.Printf("networkd: rollback token=%s restore units: %v", token, err)
		return err
	}
	if _, err := a.run.run("networkctl", "reload"); err != nil {
		log.Printf("networkd: rollback token=%s: %v", token, err)
		return err
	}
	// Reload re-reads the restored units; reconfigure re-applies them to the live
	// links so the revert actually takes (the lock-out's whole point).
	for _, link := range linksOf(snap) {
		if _, err := a.run.run("networkctl", "reconfigure", link); err != nil {
			log.Printf("networkd: rollback token=%s reconfigure %s (best-effort): %v", token, link, err)
		}
	}
	return nil
}

// Live reads the box's current networking — the base netd diffs an edit against.
func (a *Applier) Live() model.Config {
	cfg, _ := readDir(a.dir)
	return cfg
}

// Devices lists the managed physical link names (eth0, …) for the FE wizards.
func (a *Applier) Devices() []string {
	out, err := a.run.run("ip", "-d", "-j", "link", "show")
	if err != nil {
		return nil
	}
	return physicalLinks(out)
}

// readDir loads the current model by parsing the unit files in dir (overridable
// for tests).
func readDir(dir string) (model.Config, error) {
	units, err := readUnits(dir)
	if err != nil {
		return model.Config{}, err
	}
	return networkdprofile.ParseUnits(units)
}

// readUnits reads every *.netdev / *.network in dir, keyed by filename.
func readUnits(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !isUnit(e.Name()) {
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

func isUnit(name string) bool {
	return strings.HasSuffix(name, ".netdev") || strings.HasSuffix(name, ".network")
}

// snapshotDir captures every unit file's contents in dir.
func snapshotDir(dir string) (map[string]string, error) { return readUnits(dir) }

// writeDir makes dir's unit files exactly match units — writing the new set and
// removing any wash-managed unit no longer present. wash owns the networkd unit
// dir in the type-1 own-it posture, so a stale .netdev/.network from a prior
// apply must not linger.
func writeDir(dir string, units map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cur, err := readUnits(dir)
	if err != nil {
		return err
	}
	for name := range cur {
		if _, keep := units[name]; !keep {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	for name, text := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// restoreDir resets dir's unit files to exactly snap (the inverse of an Apply).
func restoreDir(dir string, snap map[string]string) error { return writeDir(dir, snap) }

// linksOf extracts the Match Name of each .network in a unit set, so Rollback
// knows which links to reconfigure after restoring.
func linksOf(units map[string]string) []string {
	cfg, err := networkdprofile.ParseUnits(units)
	if err != nil {
		return nil
	}
	return affectedLinks(cfg)
}
