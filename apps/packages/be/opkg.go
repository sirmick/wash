package packages

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// opkgBackend wraps OpenWRT's opkg. Single required binary; installed-
// state comes from `opkg list-installed`. opkg has no per-package
// non-interactive flag (no -y / --assumeyes); priv runs it directly
// and the user accepts the prompts inside the PTY widget.
type opkgBackend struct{}

func newOPKG() Backend {
	if _, err := exec.LookPath(opkgBin()); err != nil {
		return nil
	}
	return &opkgBackend{}
}

func (*opkgBackend) Name() string { return "opkg" }

// Search runs `opkg find '*query*'` for name-version-summary matches,
// then folds in `opkg list-installed` for installed-state. opkg's
// `find` defaults to glob matching; bracketing the query in '*' makes
// it substring-style like apt-cache search.
func (b *opkgBackend) Search(ctx context.Context, query string) ([]Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, opkgBin(), "find", "*"+query+"*").Output()
	if err != nil {
		return nil, fmt.Errorf("opkg find: %w", err)
	}

	installed, err := opkgInstalled(ctx)
	if err != nil {
		installed = nil
	}

	return parseOPKGList(out, installed), nil
}

// parseOPKGList reads `opkg find` (or `opkg list`) output. Each line:
//
//	<name> - <version> - <summary>
//
// `list-installed` has the same shape minus the summary; both parse
// through this function with a per-call summary fallback.
func parseOPKGList(b []byte, installed map[string]string) []Package {
	var packages []Package
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " - ", 3)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		version := strings.TrimSpace(parts[1])
		summary := ""
		if len(parts) == 3 {
			summary = strings.TrimSpace(parts[2])
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		p := Package{Name: name, Version: version, Summary: summary}
		if v, ok := installed[name]; ok {
			p.Installed = true
			if v != "" {
				p.Version = v
			}
		}
		packages = append(packages, p)
	}
	return packages
}

// opkgInstalled enumerates installed packages via `opkg list-installed`.
// Returns name → version map.
func opkgInstalled(ctx context.Context) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, opkgBin(), "list-installed").Output()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) == 2 {
			m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return m, scanner.Err()
}

// opkgBin returns the opkg binary path. Override via WASH_PACKAGES_OPKG_BIN
// so the e2e suite can substitute /bin/echo and exercise the action
// wire without a real opkg invocation.
func opkgBin() string {
	if v := os.Getenv("WASH_PACKAGES_OPKG_BIN"); v != "" {
		return v
	}
	return "opkg"
}

func (*opkgBackend) InstallArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{opkgBin(), "install", pkg}
}

func (*opkgBackend) RemoveArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{opkgBin(), "remove", pkg}
}

func (*opkgBackend) UpgradePkgArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{opkgBin(), "upgrade", pkg}
}

func (*opkgBackend) GlobalActions() []GlobalAction {
	return []GlobalAction{
		{ID: "update_system", Label: "Update system",
			Desc: "opkg update && opkg upgrade — refresh and upgrade all",
			Surface: "toolbar"},
		{ID: "update", Label: "Update index",
			Desc: "opkg update"},
		{ID: "upgrade", Label: "Upgrade",
			Desc: "opkg upgrade — apply available upgrades"},
	}
}

func (*opkgBackend) GlobalActionArgv(id string) []string {
	switch id {
	case "update":
		return []string{opkgBin(), "update"}
	case "upgrade":
		return []string{opkgBin(), "upgrade"}
	case "update_system":
		bin := opkgBin()
		return []string{"sh", "-c", bin + " update && " + bin + " upgrade"}
	}
	return nil
}
