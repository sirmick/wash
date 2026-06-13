package ifupdown

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/ifupdownprofile"
	"github.com/sirmick/wash/internal/washnet/model"
)

// Applier is the backend.Applier over Debian ifupdown. Takeover: Apply snapshots
// /etc/network/interfaces, overwrites it with wash's rendered file, and bounces
// the interfaces with ifdown/ifup. path, run, and route are overridable for
// tests.
type Applier struct {
	path  string
	run   runner
	route func() bool

	settle   time.Duration
	window   time.Duration
	interval time.Duration

	mu           sync.Mutex
	seq          int
	snapshots    map[backend.RollbackToken]string // the prior file contents (or "" = absent)
	lastHadRoute bool
}

// NewApplier builds an ifupdown-backed Applier owning /etc/network/interfaces.
func NewApplier() *Applier {
	return &Applier{
		path:      InterfacesPath,
		run:       execRunner{},
		route:     hasDefaultRoute,
		settle:    time.Second,
		window:    14 * time.Second,
		interval:  500 * time.Millisecond,
		snapshots: map[backend.RollbackToken]string{},
	}
}

func (a *Applier) Capabilities() caps.Capabilities { return Capabilities() }

func (a *Applier) Render(c model.Config) (backend.Artifacts, error) {
	files, err := ifupdownprofile.Render(c)
	if err != nil {
		return nil, err
	}
	return backend.Artifacts(files), nil
}

// Apply snapshots the interfaces file, writes wash's version, and bounces the
// stack (ifdown -a then ifup -a). Best-effort on the down (an iface that isn't
// up errors harmlessly).
func (a *Applier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	files, err := ifupdownprofile.Render(p.Target)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	snap := readFile(a.path)
	a.seq++
	token := backend.RollbackToken("ifupdown-" + strconv.Itoa(a.seq))
	a.snapshots[token] = snap
	a.lastHadRoute = a.route()
	a.mu.Unlock()

	log.Printf("ifupdown: apply token=%s writing %s, bouncing interfaces", token, a.path)
	if err := os.WriteFile(a.path, []byte(files[ifupdownprofile.FileName]), 0o644); err != nil {
		return token, err
	}
	// ifdown is best-effort (an interface that was never up errors
	// harmlessly) but not silent — its output is the trail when the
	// bounce strands the box.
	if out, err := a.run.run("ifdown", "-a"); err != nil {
		log.Printf("ifupdown: apply token=%s ifdown -a (best-effort): %v output=%q", token, err, out)
	}
	if _, err := a.run.run("ifup", "-a"); err != nil {
		log.Printf("ifupdown: apply token=%s: %v", token, err)
		return token, err
	}
	return token, nil
}

// Verify is the commit-confirm lock-out check: losing the prior default route
// triggers an auto-revert.
func (a *Applier) Verify(model.Config) error {
	a.mu.Lock()
	hadRoute := a.lastHadRoute
	a.mu.Unlock()
	if !hadRoute {
		return nil
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
	return fmt.Errorf("verify: lost the default route after ifup — reverting (lock-out protection)")
}

func (a *Applier) Confirm(token backend.RollbackToken) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.snapshots, token)
	return nil
}

// Rollback restores the snapshotted interfaces file and bounces the stack.
func (a *Applier) Rollback(token backend.RollbackToken) error {
	a.mu.Lock()
	snap, ok := a.snapshots[token]
	delete(a.snapshots, token)
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("rollback: unknown token %q", token)
	}
	log.Printf("ifupdown: rollback token=%s restoring %s", token, a.path)
	if snap == "" {
		if err := os.Remove(a.path); err != nil && !os.IsNotExist(err) {
			log.Printf("ifupdown: rollback token=%s remove %s: %v", token, a.path, err)
		}
	} else if err := os.WriteFile(a.path, []byte(snap), 0o644); err != nil {
		log.Printf("ifupdown: rollback token=%s restore %s FAILED: %v", token, a.path, err)
		return err
	}
	if out, err := a.run.run("ifdown", "-a"); err != nil {
		log.Printf("ifupdown: rollback token=%s ifdown -a (best-effort): %v output=%q", token, err, out)
	}
	_, err := a.run.run("ifup", "-a")
	if err != nil {
		log.Printf("ifupdown: rollback token=%s: %v", token, err)
	}
	return err
}

// Live parses /etc/network/interfaces into the model.
func (a *Applier) Live() model.Config {
	cfg, _ := ifupdownprofile.Parse(map[string]string{ifupdownprofile.FileName: readFile(a.path)})
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

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
