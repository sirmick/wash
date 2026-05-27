package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// systemdBackend wraps `systemctl` + `/run/systemd/system` detection.
// Two reads per List: `list-units` (load/active/sub/description in
// one JSON pass) and `list-unit-files` (enabled state, which the
// first call doesn't carry).
type systemdBackend struct{}

func newSystemd() Backend {
	if _, err := exec.LookPath(systemctlBin()); err != nil {
		return nil
	}
	// systemctl can be present on boxes that aren't running systemd
	// as PID 1 (some Docker images). Require the runtime dir so we
	// don't claim systemd on a box that would just error on every
	// list-units call.
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return nil
	}
	return &systemdBackend{}
}

func (*systemdBackend) Name() string     { return "systemd" }
func (*systemdBackend) LogAppID() string { return "com.wash.journal" }

func (*systemdBackend) List(ctx context.Context) ([]Service, error) {
	unitsOut, err := exec.CommandContext(ctx, systemctlBin(),
		"list-units", "--type=service", "--all",
		"--no-legend", "--plain", "--no-pager", "--output=json").Output()
	if err != nil {
		return nil, fmt.Errorf("list-units: %w", err)
	}
	rows, err := parseSystemdUnits(unitsOut)
	if err != nil {
		return nil, err
	}

	// list-unit-files for the enabled/disabled state — list-units
	// doesn't carry it. Soft-fail: if this call errors we still
	// return rows with empty Enabled fields rather than dropping
	// the whole list.
	enabled := map[string]string{}
	if filesOut, err := exec.CommandContext(ctx, systemctlBin(),
		"list-unit-files", "--type=service",
		"--no-legend", "--plain", "--no-pager").Output(); err == nil {
		enabled = parseSystemdUnitFiles(filesOut)
	}

	services := make([]Service, 0, len(rows)+len(enabled))
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.Unit] = true
		services = append(services, Service{
			Name:        r.Unit,
			Description: r.Description,
			Load:        r.Load,
			Active:      r.Active,
			Sub:         r.Sub,
			Enabled:     enabled[r.Unit],
		})
	}
	// Installed-but-not-loaded units: include so the user can enable
	// or start them without first knowing they exist.
	for name, e := range enabled {
		if seen[name] {
			continue
		}
		services = append(services, Service{
			Name:    name,
			Active:  "inactive",
			Sub:     "dead",
			Load:    "not-loaded",
			Enabled: e,
		})
	}
	return services, nil
}

func (*systemdBackend) ActionArgv(op, name string) []string {
	switch op {
	case "start", "stop", "restart", "reload", "enable", "disable":
		return []string{systemctlBin(), op, name}
	}
	return nil
}

// ----- pure parsers (testable without exec) -----

// systemdUnitRow mirrors one entry from `systemctl list-units
// --output=json`. Fields are PascalCase on the wire.
type systemdUnitRow struct {
	Unit, Load, Active, Sub, Description string
}

func parseSystemdUnits(b []byte) ([]systemdUnitRow, error) {
	var rows []systemdUnitRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("list-units json: %w", err)
	}
	return rows, nil
}

// parseSystemdUnitFiles parses the `list-unit-files` text output.
// Each line is "<unit> <state> [<preset>]" — we only need the first
// two columns.
func parseSystemdUnitFiles(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			out[fields[0]] = fields[1]
		}
	}
	return out
}

// systemctlBin returns the systemctl path. Override via
// WASH_SERVICES_SYSTEMCTL_BIN for unit tests that need a canned
// output stub.
func systemctlBin() string {
	if v := os.Getenv("WASH_SERVICES_SYSTEMCTL_BIN"); v != "" {
		return v
	}
	return "systemctl"
}
