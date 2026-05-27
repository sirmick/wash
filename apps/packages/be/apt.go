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

// aptBackend wraps Debian/Ubuntu's apt-get for package actions and
// optionally tasksel for "task" collections (lamp-server, openssh-
// server, …). apt-get is required; tasksel is best-effort — if it's
// missing, the backend just doesn't surface task rows in search.
// Actions run non-interactive (-y, DEBIAN_FRONTEND=noninteractive)
// so the terminal widget shows progress without blocking on prompts.
type aptBackend struct {
	hasTasksel bool // cached at construction; gates task search merge
}

func newAPT() Backend {
	for _, bin := range []string{"apt-get", "apt-cache", "dpkg-query"} {
		if _, err := exec.LookPath(bin); err != nil {
			return nil
		}
	}
	_, taskErr := exec.LookPath(taskselBin())
	return &aptBackend{hasTasksel: taskErr == nil}
}

func (*aptBackend) Name() string { return "apt" }

// Search runs `apt-cache search <query>` for the name+summary list,
// then `dpkg-query -W` once to fold in installed-state and the
// candidate version. apt-cache's --names-only would speed things up
// but loses substring matches in package descriptions; keep the
// fuller search behaviour and let the FE debounce.
//
// When tasksel is available, tasksel tasks matching the query are
// merged in as Kind="task" rows alongside regular packages so a
// user searching "ssh" sees openssh-server (the task) next to the
// individual openssh-* packages.
func (b *aptBackend) Search(ctx context.Context, query string) ([]Package, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "apt-cache", "search", "--names-only", query).Output()
	if err != nil {
		return nil, fmt.Errorf("apt-cache search: %w", err)
	}

	installed, err := dpkgInstalled(ctx)
	if err != nil {
		// Best-effort: a missing dpkg-query just means we lose
		// installed-state, not that search fails.
		installed = nil
	}

	var packages []Package
	scanner := bufio.NewScanner(bytes.NewReader(out))
	// apt-cache search lines are "<name> - <summary>". Names with
	// hyphens are fine; we split on the first " - " separator.
	for scanner.Scan() {
		line := scanner.Text()
		sep := strings.Index(line, " - ")
		var name, summary string
		if sep < 0 {
			name = line
		} else {
			name = line[:sep]
			summary = line[sep+3:]
		}
		if name == "" {
			continue
		}
		p := Package{Name: name, Summary: summary}
		if v, ok := installed[name]; ok {
			p.Installed = true
			p.Version = v
		}
		packages = append(packages, p)
	}
	if err := scanner.Err(); err != nil {
		return packages, fmt.Errorf("scan apt-cache: %w", err)
	}

	if b.hasTasksel {
		tasks, taskErr := taskselTasks(ctx, query)
		if taskErr == nil {
			// Prepend tasks so collection-style units bubble to the
			// top of the list — they're usually what a search like
			// "openssh" or "lamp" is reaching for.
			packages = append(tasks, packages...)
		}
	}
	return packages, nil
}

// dpkgInstalled enumerates packages with Status starting "install ok
// installed" — the canonical dpkg state for "currently installed".
// "rc" entries (removed but config remains) are intentionally
// excluded so the FE doesn't show them as installed.
func dpkgInstalled(ctx context.Context) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Package}\t${Version}\t${Status}\n").Output()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 3)
		if len(fields) != 3 {
			continue
		}
		if !strings.HasPrefix(fields[2], "install ok installed") {
			continue
		}
		m[fields[0]] = fields[1]
	}
	return m, scanner.Err()
}

// aptBin returns the apt-get binary path. Override via
// WASH_PACKAGES_APT_BIN so the e2e suite can substitute /bin/echo
// (or any harmless stand-in) and exercise the full action wire path
// without root or a real apt-get invocation.
func aptBin() string {
	if v := os.Getenv("WASH_PACKAGES_APT_BIN"); v != "" {
		return v
	}
	return "apt-get"
}

// taskselBin mirrors aptBin — override via WASH_PACKAGES_TASKSEL_BIN
// for tests that don't have tasksel installed (or want a canned
// stub of --list-tasks output).
func taskselBin() string {
	if v := os.Getenv("WASH_PACKAGES_TASKSEL_BIN"); v != "" {
		return v
	}
	return "tasksel"
}

// Action argv. apt-get -y handles default-Yes for prompts; setting
// DEBIAN_FRONTEND=noninteractive in the priv env (the caller does
// this when spawning) keeps debconf from blocking on dialogs.
//
// kind="task" routes to tasksel, which has its own install/remove
// verbs and no per-task upgrade. Anything else is treated as a
// regular package.
func (*aptBackend) InstallArgv(pkg, kind string) []string {
	if kind == "task" {
		return []string{taskselBin(), "install", pkg}
	}
	return []string{aptBin(), "-y", "install", pkg}
}
func (*aptBackend) RemoveArgv(pkg, kind string) []string {
	if kind == "task" {
		return []string{taskselBin(), "remove", pkg}
	}
	return []string{aptBin(), "-y", "remove", pkg}
}
func (*aptBackend) UpgradePkgArgv(pkg, kind string) []string {
	if kind == "task" {
		// tasksel has no per-task upgrade — packages inside a task
		// upgrade individually via the regular per-package path.
		return nil
	}
	return []string{aptBin(), "-y", "install", "--only-upgrade", pkg}
}
// GlobalActions enumerates the apt-wide actions surfaced in the
// FE. "update_system" is the toolbar combo (refresh-then-upgrade,
// the "I just want everything current" button); the other four
// are the granular ops the menu exposes for users who want
// control. The shell-combo argv keeps both halves in one PTY so
// the user sees a continuous stream of output rather than two
// separate priv requests.
func (*aptBackend) GlobalActions() []GlobalAction {
	return []GlobalAction{
		{ID: "update_system", Label: "Update system",
			Desc: "apt-get update && apt-get dist-upgrade", Surface: "toolbar"},
		{ID: "update", Label: "Update package index",
			Desc: "apt-get update"},
		{ID: "upgrade", Label: "Upgrade (safe)",
			Desc: "apt-get upgrade — no add/remove"},
		{ID: "dist_upgrade", Label: "Dist-upgrade",
			Desc: "apt-get dist-upgrade — may add/remove dependencies"},
		{ID: "autoremove", Label: "Autoremove orphans",
			Desc: "apt-get autoremove", Danger: true},
	}
}

func (*aptBackend) GlobalActionArgv(id string) []string {
	switch id {
	case "update":
		return []string{aptBin(), "update"}
	case "upgrade":
		return []string{aptBin(), "-y", "upgrade"}
	case "dist_upgrade":
		return []string{aptBin(), "-y", "dist-upgrade"}
	case "autoremove":
		return []string{aptBin(), "-y", "autoremove"}
	case "update_system":
		// Combo: shell-chain so both halves stream into the same
		// PTY (one priv approval, one continuous log). Use the
		// caller-overridable apt binary so test stubs work the
		// same way as the granular actions.
		bin := aptBin()
		return []string{"sh", "-c", bin + " update && " + bin + " -y dist-upgrade"}
	}
	return nil
}

// taskselTasks runs `tasksel --list-tasks` and filters by query
// (case-insensitive substring match on name or description). Output
// format is "<status>\t<name>\t<description>" — status is `i`
// (installed), `u` (uninstalled), `+`/`-` (pending select/deselect,
// rare). Anything that's `i` becomes Installed=true.
func taskselTasks(ctx context.Context, query string) ([]Package, error) {
	out, err := exec.CommandContext(ctx, taskselBin(), "--list-tasks").Output()
	if err != nil {
		return nil, fmt.Errorf("tasksel --list-tasks: %w", err)
	}
	q := strings.ToLower(query)
	var tasks []Package
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 3)
		if len(fields) < 2 {
			continue
		}
		status := strings.TrimSpace(fields[0])
		name := strings.TrimSpace(fields[1])
		desc := ""
		if len(fields) == 3 {
			desc = strings.TrimSpace(fields[2])
		}
		if name == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(name), q) && !strings.Contains(strings.ToLower(desc), q) {
			continue
		}
		tasks = append(tasks, Package{
			Name:      name,
			Summary:   desc,
			Installed: status == "i",
			Kind:      "task",
		})
	}
	return tasks, scanner.Err()
}
