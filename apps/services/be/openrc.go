package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// openrcBackend wraps OpenRC. Three reads per List: walk /etc/init.d
// for the catalog, `rc-status -a -f ini` for live state, and
// `rc-update show -v` for enabled (any-runlevel) state.
type openrcBackend struct{}

func newOpenRC() Backend {
	if _, err := exec.LookPath(rcServiceBin()); err != nil {
		return nil
	}
	return &openrcBackend{}
}

func (*openrcBackend) Name() string     { return "openrc" }
func (*openrcBackend) LogAppID() string { return "com.wash.syslogs" }

func (*openrcBackend) List(ctx context.Context) ([]Service, error) {
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

	running := map[string]string{}
	if out, err := exec.CommandContext(ctx, rcStatusBin(), "-a", "-f", "ini").Output(); err == nil {
		running = parseRcStatus(out)
	}
	enabled := map[string]bool{}
	if out, err := exec.CommandContext(ctx, rcUpdateBin(), "show", "-v").Output(); err == nil {
		enabled = parseRcUpdate(out)
	}

	for i := range services {
		applyOpenRCState(&services[i], running, enabled)
	}
	return services, nil
}

func (*openrcBackend) ActionArgv(op, name string) []string {
	switch op {
	case "start", "stop", "restart":
		return []string{rcServiceBin(), name, op}
	case "enable":
		return []string{rcUpdateBin(), "add", name}
	case "disable":
		return []string{rcUpdateBin(), "del", name}
	}
	return nil
}

// ----- pure parsers (testable without exec) -----

// parseRcStatus parses the `rc-status -a -f ini` output. Groups
// services under [runlevel] headers; "name = status" lines under
// each. A single name may appear in multiple runlevels —
// last-write-wins is fine because status is the live state, not
// per-runlevel.
func parseRcStatus(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		out[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return out
}

// parseRcUpdate parses `rc-update show -v`. Each line is
// "name | runlevel [runlevel ...]" — the service is enabled if the
// runlevel column is non-empty for any runlevel.
func parseRcUpdate(b []byte) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		rls := strings.TrimSpace(parts[1])
		if name != "" && rls != "" {
			out[name] = true
		}
	}
	return out
}

// applyOpenRCState mutates s in place with the live status + enabled
// state from the parser maps. Kept as a separate function so the
// status-mapping logic is unit-testable.
func applyOpenRCState(s *Service, running map[string]string, enabled map[string]bool) {
	if status, ok := running[s.Name]; ok {
		s.Sub = status
		switch status {
		case "started", "running":
			s.Active = "active"
		case "crashed":
			s.Active = "failed"
		default:
			s.Active = "inactive"
		}
	} else {
		s.Active = "inactive"
		s.Sub = "stopped"
	}
	if enabled[s.Name] {
		s.Enabled = "enabled"
	} else {
		s.Enabled = "disabled"
	}
}

// ----- binary path + dir overrides -----

func rcServiceBin() string {
	if v := os.Getenv("WASH_SERVICES_RC_SERVICE_BIN"); v != "" {
		return v
	}
	return "rc-service"
}

func rcStatusBin() string {
	if v := os.Getenv("WASH_SERVICES_RC_STATUS_BIN"); v != "" {
		return v
	}
	return "rc-status"
}

func rcUpdateBin() string {
	if v := os.Getenv("WASH_SERVICES_RC_UPDATE_BIN"); v != "" {
		return v
	}
	return "rc-update"
}

func initdDir() string {
	if v := os.Getenv("WASH_SERVICES_INITD_DIR"); v != "" {
		return v
	}
	return "/etc/init.d"
}
