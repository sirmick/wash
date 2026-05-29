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

// dnfBackend wraps Fedora's dnf (and rpm for installed-state). dnf is
// required; rpm is required (ships with every dnf install). Action
// argv uses `-y` for non-interactive runs in the priv-driven PTY.
type dnfBackend struct{}

func newDNF() Backend {
	for _, bin := range []string{"dnf", "rpm"} {
		if _, err := exec.LookPath(bin); err != nil {
			return nil
		}
	}
	return &dnfBackend{}
}

func (*dnfBackend) Name() string { return "dnf" }

// Search runs `dnf search --quiet <query>` for name+summary, then
// `rpm -qa --qf '%{NAME}\t%{VERSION}-%{RELEASE}\n'` once to fold in
// installed-state and the installed version. dnf's own
// `list installed` overlaps with rpm but emits headers/footers; rpm
// is the cleaner read.
func (b *dnfBackend) Search(ctx context.Context, query string) ([]Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, dnfBin(),
		"--quiet", "search", query).Output()
	if err != nil {
		// dnf exits 1 when no matches; surface as empty list, not error.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("dnf search: %w", err)
	}

	installed, err := rpmInstalled(ctx)
	if err != nil {
		// Best-effort: missing rpm loses installed-state, not the list.
		installed = nil
	}

	packages := parseDNFSearch(out, installed)
	return packages, nil
}

// parseDNFSearch reads `dnf search --quiet` output. Each match is
// "<name>.<arch> : <summary>" — `dnf search` groups results under
// "=== Name Exactly Matched: <query> ===" headers we skip.
func parseDNFSearch(b []byte, installed map[string]string) []Package {
	var packages []Package
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "===") {
			continue
		}
		// Format: "name.arch : summary". The first " : " separates
		// the qualified package name from the summary.
		sep := strings.Index(line, " : ")
		if sep < 0 {
			continue
		}
		nameArch := strings.TrimSpace(line[:sep])
		summary := strings.TrimSpace(line[sep+3:])
		// Strip the trailing ".<arch>" (.x86_64, .noarch, …). We want
		// the bare package name so installed-state lookup works.
		name := nameArch
		if dot := strings.LastIndex(nameArch, "."); dot > 0 {
			arch := nameArch[dot+1:]
			if isRPMArch(arch) {
				name = nameArch[:dot]
			}
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		p := Package{Name: name, Summary: summary}
		if v, ok := installed[name]; ok {
			p.Installed = true
			p.Version = v
		}
		packages = append(packages, p)
	}
	return packages
}

// isRPMArch returns true for known RPM arch suffixes. Anything else
// is treated as part of the package name (e.g. python3.11 has a dot
// but ".11" isn't an arch).
func isRPMArch(s string) bool {
	switch s {
	case "x86_64", "i686", "aarch64", "armv7hl", "ppc64le",
		"s390x", "riscv64", "noarch", "src":
		return true
	}
	return false
}

// rpmInstalled enumerates installed packages via rpm -qa. Returns a
// map of name → version-release for installed-state lookup.
func rpmInstalled(ctx context.Context) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "rpm",
		"-qa", "--qf", "%{NAME}\t%{VERSION}-%{RELEASE}\n").Output()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 2)
		if len(fields) != 2 {
			continue
		}
		m[fields[0]] = fields[1]
	}
	return m, scanner.Err()
}

// dnfBin returns the dnf binary path. Override via WASH_PACKAGES_DNF_BIN
// so the e2e suite can substitute /bin/echo and exercise the full
// action wire without a real dnf invocation.
func dnfBin() string {
	if v := os.Getenv("WASH_PACKAGES_DNF_BIN"); v != "" {
		return v
	}
	return "dnf"
}

func (*dnfBackend) InstallArgv(pkg, kind string) []string {
	// dnf has groups (`dnf group install`) but we don't surface them
	// in search yet; reject explicitly so the kind=="" path stays the
	// only supported shape.
	if kind != "" {
		return nil
	}
	return []string{dnfBin(), "-y", "install", pkg}
}

func (*dnfBackend) RemoveArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{dnfBin(), "-y", "remove", pkg}
}

func (*dnfBackend) UpgradePkgArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{dnfBin(), "-y", "upgrade", pkg}
}

func (*dnfBackend) GlobalActions() []GlobalAction {
	return []GlobalAction{
		{ID: "update_system", Label: "Update system",
			Desc: "dnf upgrade — refresh metadata and upgrade everything",
			Surface: "toolbar"},
		{ID: "refresh", Label: "Refresh metadata",
			Desc: "dnf check-update — refresh the package index"},
		{ID: "upgrade", Label: "Upgrade",
			Desc: "dnf upgrade — apply available upgrades"},
		{ID: "autoremove", Label: "Autoremove orphans",
			Desc: "dnf autoremove", Danger: true},
		{ID: "clean", Label: "Clean cache",
			Desc: "dnf clean all"},
	}
}

func (*dnfBackend) GlobalActionArgv(id string) []string {
	switch id {
	case "refresh":
		return []string{dnfBin(), "check-update"}
	case "upgrade", "update_system":
		// dnf upgrade implicitly refreshes metadata, so a single call
		// covers the toolbar combo without an `sh -c` chain.
		return []string{dnfBin(), "-y", "upgrade"}
	case "autoremove":
		return []string{dnfBin(), "-y", "autoremove"}
	case "clean":
		return []string{dnfBin(), "clean", "all"}
	}
	return nil
}
