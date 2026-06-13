package uci

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
)

// Applier is the backend.Applier over OpenWRT/UCI. Takeover: Apply snapshots the
// managed /etc/config/* files, writes wash's rendered versions, and reloads the
// touched services. dir/run/route are overridable for hermetic tests.
//
// Phase 1 writes only the packages codec.Render emitted (the model has objects
// for them) and leaves the rest untouched — so editing just the network never
// clobbers an existing /etc/config/firewall. Once the router UI makes the model
// authoritative over every package, this can become a wholesale rewrite (empty a
// package to remove all its objects); the snapshot already covers every managed
// package for rollback either way.
type Applier struct {
	dir   string
	run   runner
	route func() bool

	settle   time.Duration
	window   time.Duration
	interval time.Duration

	mu           sync.Mutex
	seq          int
	snapshots    map[backend.RollbackToken]map[string]string // pkg → prior text ("" = absent)
	lastHadRoute bool
}

// NewApplier builds a UCI-backed Applier owning /etc/config.
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
	files, err := codec.Render(c)
	if err != nil {
		return nil, err
	}
	return backend.Artifacts(files), nil
}

// Apply snapshots the managed packages, writes the rendered ones, and reloads
// each touched service.
func (a *Applier) Apply(p backend.RenderPlan) (backend.RollbackToken, error) {
	files, err := codec.Render(p.Target)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	snap := a.snapshot()
	a.seq++
	token := backend.RollbackToken("uci-" + strconv.Itoa(a.seq))
	a.snapshots[token] = snap
	a.lastHadRoute = a.route()
	a.mu.Unlock()

	var touched []string
	for _, pkg := range managedPackages {
		text, ok := files[pkg]
		if !ok {
			continue // model has no objects for this package — leave it as-is
		}
		if err := a.writePkg(pkg, text); err != nil {
			log.Printf("uci: apply token=%s write pkg=%s: %v", token, pkg, err)
			return token, err
		}
		touched = append(touched, pkg)
	}
	// Log the planned reload sequence BEFORE executing it — reload
	// ordering is exactly what goes wrong when an apply half-takes,
	// and the trail must exist even if a reload wedges the box.
	log.Printf("uci: apply token=%s wrote pkgs=%v, reload order=%v", token, touched, reloadPlan(touched))
	for _, pkg := range touched {
		if cmd := reloadCmd[pkg]; len(cmd) > 0 {
			if out, err := a.run.run(cmd[0], cmd[1:]...); err != nil {
				log.Printf("uci: apply token=%s reload pkg=%s (best-effort): %v output=%q", token, pkg, err, out)
			}
		}
	}
	return token, nil
}

// reloadPlan renders the reload commands that will run for the touched
// packages, in execution order, for the pre-reload log line.
func reloadPlan(pkgs []string) []string {
	var plan []string
	for _, pkg := range pkgs {
		if cmd := reloadCmd[pkg]; len(cmd) > 0 {
			plan = append(plan, pkg+":"+strings.Join(cmd, " "))
		}
	}
	return plan
}

// Verify is the commit-confirm lock-out check shared by the file-renderer
// backends: if the box had a default route before the apply, confirm it's still
// there after a settle + a short consecutive-sample streak.
func (a *Applier) Verify(model.Config) error {
	a.mu.Lock()
	had := a.lastHadRoute
	settle, window, interval := a.settle, a.window, a.interval
	a.mu.Unlock()
	if !had {
		return nil
	}
	time.Sleep(settle)
	deadline := time.Now().Add(window)
	streak := 0
	for time.Now().Before(deadline) {
		if a.route() {
			if streak++; streak >= 3 {
				return nil
			}
		} else {
			streak = 0
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("lost the default route after apply")
}

func (a *Applier) Confirm(token backend.RollbackToken) error {
	a.mu.Lock()
	delete(a.snapshots, token)
	a.mu.Unlock()
	return nil
}

// Rollback restores every snapshotted package and reloads.
func (a *Applier) Rollback(token backend.RollbackToken) error {
	a.mu.Lock()
	snap, ok := a.snapshots[token]
	delete(a.snapshots, token)
	a.mu.Unlock()
	if !ok {
		return nil
	}
	log.Printf("uci: rollback token=%s restoring pkgs=%d, reload order=%v", token, len(snap), reloadPlan(managedPackages))
	for _, pkg := range managedPackages {
		if text, ok := snap[pkg]; ok {
			if err := a.writePkg(pkg, text); err != nil {
				// A rollback that can't restore a package is the worst
				// state the box can be in — it must never be silent.
				log.Printf("uci: rollback token=%s restore pkg=%s FAILED: %v", token, pkg, err)
			}
		}
	}
	for _, pkg := range managedPackages {
		if cmd := reloadCmd[pkg]; len(cmd) > 0 {
			if out, err := a.run.run(cmd[0], cmd[1:]...); err != nil {
				log.Printf("uci: rollback token=%s reload pkg=%s (best-effort): %v output=%q", token, pkg, err, out)
			}
		}
	}
	return nil
}

// Live reads the managed packages and parses them back into the model.
func (a *Applier) Live() model.Config {
	files := map[string]string{}
	for _, pkg := range managedPackages {
		if b, err := os.ReadFile(filepath.Join(a.dir, pkg)); err == nil {
			files[pkg] = string(b)
		}
	}
	cfg, _ := codec.Parse(files)
	return cfg
}

// Devices lists the box's real NICs for the Add wizards.
func (a *Applier) Devices() []string {
	out, err := a.run.run("ip", "-d", "-j", "link", "show")
	if err != nil {
		return nil
	}
	return physicalLinks(out)
}

// snapshot captures the current text of every managed package (absent → "").
func (a *Applier) snapshot() map[string]string {
	snap := map[string]string{}
	for _, pkg := range managedPackages {
		if b, err := os.ReadFile(filepath.Join(a.dir, pkg)); err == nil {
			snap[pkg] = string(b)
		} else {
			snap[pkg] = ""
		}
	}
	return snap
}

func (a *Applier) writePkg(pkg, text string) error {
	return os.WriteFile(filepath.Join(a.dir, pkg), []byte(text), 0o644)
}
