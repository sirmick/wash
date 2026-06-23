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

// apkBackend wraps Alpine's apk. apk is the single required binary;
// installed-state comes from `apk info -e` which is cheap. Action
// argv uses `--no-progress` so the PTY stream stays line-oriented
// instead of redrawing a spinner.
type apkBackend struct{}

func newAPK() Backend {
	if _, err := exec.LookPath(apkBin()); err != nil {
		return nil
	}
	return &apkBackend{}
}

func (*apkBackend) Name() string { return "apk" }

// Search runs `apk search -v <query>` for name-version-summary, then
// folds in `apk info -e` for installed-state. apk's search emits one
// line per match in the format "name-version description". We split
// the trailing description off the version.
func (b *apkBackend) Search(ctx context.Context, query string) ([]Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, apkBin(),
		"search", "-v", query).Output()
	if err != nil {
		return nil, fmt.Errorf("apk search: %w", err)
	}

	installed, err := apkInstalled(ctx)
	if err != nil {
		installed = nil
	}

	return parseAPKSearch(out, installed), nil
}

// parseAPKSearch reads `apk search -v` output. Each line is:
//
//	<name>-<version>-<release> - <summary>
//
// The version may contain hyphens (e.g. 1.2.3-r4), so we work from
// the right: find " - " (with surrounding spaces) for the summary
// boundary, then walk back from the end for the version segment.
func parseAPKSearch(b []byte, installed map[string]string) []Package {
	var packages []Package
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Split name-version from summary.
		head := line
		summary := ""
		if sep := strings.Index(line, " - "); sep >= 0 {
			head = strings.TrimSpace(line[:sep])
			summary = strings.TrimSpace(line[sep+3:])
		}
		// Split name from version. apk versions look like
		// "1.2.3-r4" — the last two hyphen-segments belong to the
		// version, everything before is the name.
		name, version := splitAPKNameVersion(head)
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

// splitAPKNameVersion separates "pkg-name-1.2.3-r4" into
// ("pkg-name", "1.2.3-r4"). Strategy: the version is the last two
// hyphen-segments where the final segment starts with "r" + digits
// (apk's release suffix). Falls back to splitting once if the
// release isn't present.
func splitAPKNameVersion(s string) (name, version string) {
	parts := strings.Split(s, "-")
	if len(parts) < 2 {
		return s, ""
	}
	// Walk back: if last segment is "rN", treat the previous segment
	// as the upstream version.
	last := parts[len(parts)-1]
	if len(last) >= 2 && last[0] == 'r' && allDigits(last[1:]) && len(parts) >= 3 {
		name = strings.Join(parts[:len(parts)-2], "-")
		version = strings.Join(parts[len(parts)-2:], "-")
		return
	}
	// No -rN suffix: drop the last segment as the version.
	name = strings.Join(parts[:len(parts)-1], "-")
	version = last
	return
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// apkInstalled enumerates installed packages via `apk info`. Returns
// a map of name → version (best-effort: `apk info` lists names; the
// version requires a second pass which we skip in the fast path).
func apkInstalled(ctx context.Context) (map[string]string, error) {
	// `apk info -v` lists installed packages as "name-version-rN".
	// We split the same way we do search output.
	out, err := exec.CommandContext(ctx, apkBin(), "info", "-v").Output()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		name, version := splitAPKNameVersion(line)
		if name != "" {
			m[name] = version
		}
	}
	return m, scanner.Err()
}

// apkBin returns the apk binary path. Override via WASH_PACKAGES_APK_BIN
// so the e2e suite can substitute /bin/echo and exercise the action
// wire without a real apk invocation.
func apkBin() string {
	if v := os.Getenv("WASH_PACKAGES_APK_BIN"); v != "" {
		return v
	}
	return "apk"
}

func (*apkBackend) InstallArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{apkBin(), "add", "--no-progress", pkg}
}

func (*apkBackend) RemoveArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	return []string{apkBin(), "del", "--no-progress", pkg}
}

func (*apkBackend) UpgradePkgArgv(pkg, kind string) []string {
	if kind != "" {
		return nil
	}
	// apk's single-package upgrade is `apk upgrade <pkg>`.
	return []string{apkBin(), "upgrade", "--no-progress", pkg}
}

func (*apkBackend) GlobalActions() []GlobalAction {
	return []GlobalAction{
		{ID: "update_system", Label: "Update system",
			Desc:    "apk update && apk upgrade — refresh index and upgrade all",
			Surface: "toolbar"},
		{ID: "update", Label: "Update index",
			Desc: "apk update"},
		{ID: "upgrade", Label: "Upgrade",
			Desc: "apk upgrade — apply available upgrades"},
		{ID: "cache_clean", Label: "Clean cache",
			Desc: "apk cache clean"},
	}
}

func (*apkBackend) GlobalActionArgv(id string) []string {
	switch id {
	case "update":
		return []string{apkBin(), "update"}
	case "upgrade":
		return []string{apkBin(), "upgrade", "--no-progress"}
	case "cache_clean":
		return []string{apkBin(), "cache", "clean"}
	case "update_system":
		bin := apkBin()
		return []string{"sh", "-c", bin + " update && " + bin + " upgrade --no-progress"}
	}
	return nil
}
