package hostmeta

import (
	"os"
	"path/filepath"
	"testing"
)

// Arch is build-derived, so it must always yield something to show.
func TestArchAlwaysSet(t *testing.T) {
	if Arch() == "" {
		t.Fatal("Arch() returned empty; must always be populated")
	}
}

// The core contract: wash runs embedded (no /proc, no /etc/os-release,
// no /etc/issue). Every file-backed reader must return "" — not panic,
// not block — when its path is absent.
func TestGracefulWhenFilesMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := kernelFrom(missing); got != "" {
		t.Errorf("kernelFrom(missing) = %q, want empty", got)
	}
	if got := osReleasePretty(missing); got != "" {
		t.Errorf("osReleasePretty(missing) = %q, want empty", got)
	}
	if got := issueLine(missing); got != "" {
		t.Errorf("issueLine(missing) = %q, want empty", got)
	}
	// Distro() composes both readers against the real (possibly absent)
	// host paths; it must not panic regardless of what's present.
	_ = Distro()
	_ = Kernel()
}

func TestKernelTrimsNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "osrelease")
	if err := os.WriteFile(p, []byte("6.17.0-35-generic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := kernelFrom(p); got != "6.17.0-35-generic" {
		t.Errorf("kernelFrom = %q", got)
	}
}

func TestOsReleasePrettyName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	body := "NAME=Ubuntu\nPRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\nID=ubuntu\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := osReleasePretty(p); got != "Ubuntu 24.04.4 LTS" {
		t.Errorf("osReleasePretty = %q", got)
	}
	// A file with no PRETTY_NAME key yields "" gracefully.
	p2 := filepath.Join(t.TempDir(), "os-release2")
	os.WriteFile(p2, []byte("ID=alpine\n"), 0o644)
	if got := osReleasePretty(p2); got != "" {
		t.Errorf("osReleasePretty(no-pretty) = %q, want empty", got)
	}
}

func TestIssueStripsGettyEscapes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "issue")
	// First line is just a "\S" escape (cleans to empty → skipped); the
	// second is a real banner with trailing \n \l escapes to strip.
	if err := os.WriteFile(p, []byte("\\S\nUbuntu 24.04 LTS \\n \\l\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := issueLine(p); got != "Ubuntu 24.04 LTS" {
		t.Errorf("issueLine = %q, want %q", got, "Ubuntu 24.04 LTS")
	}
}
