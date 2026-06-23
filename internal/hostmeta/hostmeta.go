// Package hostmeta gathers a few read-only facts about the host the
// process runs on — CPU architecture, kernel release, and distro name —
// shared by the desktop info banner (com.wash.session) and the About
// window (com.wash.about).
//
// Every function is best-effort and MUST fail gracefully: wash also runs
// embedded (microVMs, the in-browser VM, minimal rootfs) where /proc may
// be sparse and /etc/os-release or /etc/issue simply don't exist. A
// missing or unreadable file yields "" (never a panic, never a blocked
// read) so the caller just omits that field and the UI hides the row.
// Only Arch() is always populated — it comes from the build, not a file.
package hostmeta

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// Real host paths. Indirected through unexported helpers that take the
// path so the tests can point them at temp files / missing paths.
const (
	kernelPath    = "/proc/sys/kernel/osrelease"
	osReleasePath = "/etc/os-release"
	issuePath     = "/etc/issue"
)

// Arch returns a friendly CPU-architecture label (e.g. "x86-64",
// "arm64") from the build's GOARCH, which matches the host for wash's
// native binaries. Always non-empty; unknown arches return raw GOARCH.
func Arch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86-64"
	case "386":
		return "x86"
	case "arm64":
		return "arm64"
	case "arm":
		return "arm"
	case "riscv64":
		return "riscv64"
	case "ppc64le":
		return "ppc64le"
	case "s390x":
		return "s390x"
	case "loong64":
		return "loong64"
	default:
		return runtime.GOARCH
	}
}

// Kernel returns the running kernel release (the `uname -r` string, e.g.
// "6.17.0-35-generic"). "" when /proc isn't available (embedded).
func Kernel() string { return kernelFrom(kernelPath) }

// Distro returns a human distribution name: /etc/os-release PRETTY_NAME,
// falling back to the first non-empty /etc/issue line (getty escapes
// stripped). "" when neither file exists (embedded / scratch rootfs).
func Distro() string {
	if p := osReleasePretty(osReleasePath); p != "" {
		return p
	}
	return issueLine(issuePath)
}

func kernelFrom(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func osReleasePretty(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
	}
	return ""
}

func issueLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, raw := range strings.Split(string(b), "\n") {
		// Drop getty escapes: a backslash followed by one letter (\n \l
		// \S \m \r …) that getty expands at login time.
		var sb strings.Builder
		skip := false
		for _, r := range raw {
			if skip {
				skip = false
				continue
			}
			if r == '\\' {
				skip = true
				continue
			}
			sb.WriteRune(r)
		}
		if s := strings.TrimSpace(sb.String()); s != "" {
			return s
		}
	}
	return ""
}
