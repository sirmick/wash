package netplan

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
	"github.com/sirmick/wash/internal/washnet/netplanprofile"
)

// Applier is the backend.Applier over netplan. Takeover posture: Apply snapshots
// all of /etc/netplan, writes wash's single document as the SOLE authority
// (foreign YAML captured in the snapshot for Rollback), and runs `netplan
// apply`. dir, run, and route are overridable for tests.
type Applier struct {
	dir   string
	run   runner
	route func() bool

	settle   time.Duration
	window   time.Duration
	interval time.Duration

	mu           sync.Mutex
	seq          int
	snapshots    map[backend.RollbackToken]map[string]string
	lastHadRoute bool
}

// NewApplier builds a netplan-backed Applier owning /etc/netplan.
func NewApplier() *Applier {
	return &Applier{
		dir:       ConfigDir,
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
	files, err := netplanprofile.Render(c)
	if err != nil {
		return nil, err
	}
	return backend.Artifacts(files), nil
}

// Apply snapshots /etc/netplan, installs wash's document as the sole file
// (takeover: every other *.yaml is removed, preserved only in the snapshot),
// then `netplan apply`.
func (a *Applier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	files, err := netplanprofile.Render(p.Target)
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
	token := backend.RollbackToken("netplan-" + strconv.Itoa(a.seq))
	a.snapshots[token] = snap
	a.lastHadRoute = a.route()
	a.mu.Unlock()

	if err := writeDir(a.dir, files); err != nil {
		return token, err
	}
	if _, err := a.run.run("netplan", "apply"); err != nil {
		return token, err
	}
	return token, nil
}

// Verify is the commit-confirm lock-out check (docs/NET.md §7): losing the
// default route the box had before the apply triggers an auto-revert. netplan
// applies asynchronously, so a settle + stable-streak poll, not one sample.
func (a *Applier) Verify(model.Config) error {
	a.mu.Lock()
	hadRoute := a.lastHadRoute
	a.mu.Unlock()
	if !hadRoute {
		return nil // nothing to lose ⇒ no lock-out risk
	}
	time.Sleep(a.settle)
	const need = 3
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
	return fmt.Errorf("verify: lost the default route after netplan apply — reverting (lock-out protection)")
}

func (a *Applier) Confirm(token backend.RollbackToken) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.snapshots, token)
	return nil
}

// Rollback restores the /etc/netplan snapshot (bringing back any foreign files)
// and re-applies.
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
	_, err := a.run.run("netplan", "apply")
	return err
}

// Live reads the box's current networking via `netplan get` — the MERGED config
// (every /etc/netplan file), so it sees the real box no matter where the
// under-renderer's keyfiles land. This is the fix for the empty-read bug.
func (a *Applier) Live() model.Config {
	out, err := a.run.run("netplan", "get")
	if err != nil {
		return model.Config{}
	}
	cfg, _ := netplanprofile.Parse(map[string]string{netplanprofile.FileName: out})
	return cfg
}

// Devices lists managed physical NICs for the FE wizards.
func (a *Applier) Devices() []string {
	out, err := a.run.run("ip", "-d", "-j", "link", "show")
	if err != nil {
		return nil
	}
	return physicalLinks(out)
}

// --- /etc/netplan file management (takeover) -------------------------------

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// readYAML reads every *.yaml / *.yml in dir, keyed by filename.
func readYAML(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
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

func snapshotDir(dir string) (map[string]string, error) { return readYAML(dir) }

// writeDir makes dir's YAML files exactly match files — TAKEOVER: every existing
// *.yaml/*.yml not in the target set is removed (wash is the sole netplan
// author). Foreign files are recoverable only from the Apply snapshot.
func writeDir(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cur, err := readYAML(dir)
	if err != nil {
		return err
	}
	for name := range cur {
		if _, keep := files[name]; !keep {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	for name, text := range files {
		// netplan refuses world/group-readable YAML (may hold secrets).
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func restoreDir(dir string, snap map[string]string) error { return writeDir(dir, snap) }
