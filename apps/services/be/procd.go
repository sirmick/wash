package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// procdBackend wraps OpenWRT's procd init. Three reads per List:
// walk /etc/init.d for the catalog, ask ubus for live running state,
// inspect /etc/rc.d/S* symlinks for enabled state.
//
// In a non-PID-1 environment (Docker test stage), ubus has no daemon
// to talk to and List degrades to "name + load=loaded + no running
// state" — still useful for catalog assertions in the matrix.
type procdBackend struct{}

func newProcd() Backend {
	// procd is OpenWRT's init. Two probes:
	//   1. /sbin/procd exists (the binary itself, not the daemon)
	//   2. /etc/init.d walkable (presence of init scripts)
	if _, err := os.Stat(procdBin()); err != nil {
		return nil
	}
	if _, err := os.Stat(initdDir()); err != nil {
		return nil
	}
	return &procdBackend{}
}

func (*procdBackend) Name() string     { return "procd" }
func (*procdBackend) LogAppID() string { return "com.wash.syslogs" }

func (*procdBackend) List(ctx context.Context) ([]Service, error) {
	entries, err := os.ReadDir(initdDir())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", initdDir(), err)
	}
	services := make([]Service, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		services = append(services, Service{Name: e.Name(), Load: "loaded"})
	}

	// Running state via ubus. Soft-fail: container test stage has
	// /sbin/procd but no ubusd, so this call errors and we end up
	// with Active="inactive" for everything (which is correct in
	// the container — no daemon = no service is running).
	running := map[string]bool{}
	if out, err := exec.CommandContext(ctx, ubusBin(), "call", "service", "list").Output(); err == nil {
		running = parseProcdRunning(out)
	}

	enabled := procdEnabled()

	for i := range services {
		name := services[i].Name
		if running[name] {
			services[i].Active = "active"
			services[i].Sub = "running"
		} else {
			services[i].Active = "inactive"
			services[i].Sub = "stopped"
		}
		if enabled[name] {
			services[i].Enabled = "enabled"
		} else {
			services[i].Enabled = "disabled"
		}
	}
	return services, nil
}

func (*procdBackend) ActionArgv(op, name string) []string {
	switch op {
	case "start", "stop", "restart", "reload", "enable", "disable":
		return []string{filepath.Join(initdDir(), name), op}
	}
	return nil
}

// ----- pure parsers (testable without exec) -----

// parseProcdRunning parses `ubus call service list` output. Top-level
// keys are service names; each has an "instances" map; an instance is
// running if instances.*.running == true. A service counts as running
// if any of its instances do.
func parseProcdRunning(b []byte) map[string]bool {
	out := map[string]bool{}
	var raw map[string]struct {
		Instances map[string]struct {
			Running bool `json:"running"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &raw); err != nil {
		return out
	}
	for name, svc := range raw {
		for _, inst := range svc.Instances {
			if inst.Running {
				out[name] = true
				break
			}
		}
	}
	return out
}

// procdEnabled walks /etc/rc.d for "S<N><name>" symlinks. The numeric
// prefix is the boot order; everything after is the init.d service
// name. K-prefixed entries (kill ordering at shutdown) are ignored —
// only S-prefix counts as "enabled at boot".
func procdEnabled() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(rcdDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "S") || len(name) < 3 {
			continue
		}
		i := 1
		for i < len(name) && name[i] >= '0' && name[i] <= '9' {
			i++
		}
		if i > 1 && i < len(name) {
			out[name[i:]] = true
		}
	}
	return out
}

// ----- binary path + dir overrides -----

func procdBin() string {
	if v := os.Getenv("WASH_SERVICES_PROCD_BIN"); v != "" {
		return v
	}
	return "/sbin/procd"
}

func ubusBin() string {
	if v := os.Getenv("WASH_SERVICES_UBUS_BIN"); v != "" {
		return v
	}
	return "ubus"
}

func rcdDir() string {
	if v := os.Getenv("WASH_SERVICES_RCD_DIR"); v != "" {
		return v
	}
	return "/etc/rc.d"
}
