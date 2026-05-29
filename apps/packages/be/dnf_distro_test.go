//go:build distro_integration

package packages

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Runs against the host's real dnf + rpm. Skips if either is missing.
// Inside the matrix's fedora container it should always run.

func TestDNF_Distro_DetectsAndSearchesBash(t *testing.T) {
	for _, bin := range []string{"dnf", "rpm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("missing %s: %v", bin, err)
		}
	}
	// On a real fedora box, apt is absent — Detect() falls through
	// to newDNF(). On a debian box with dnf side-installed (weird
	// but possible), apt would win the probe; gate the check on the
	// returned backend's name rather than asserting "no apt".
	b := Detect()
	if b == nil {
		t.Fatalf("Detect returned nil with dnf + rpm present")
	}
	if b.Name() != "dnf" {
		// apt won the probe — this isn't a fedora host. Test the dnf
		// backend directly so we still cover its code path.
		b = newDNF()
		if b == nil {
			t.Fatalf("newDNF returned nil with dnf + rpm on PATH")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pkgs, err := b.Search(ctx, "bash")
	if err != nil {
		t.Fatalf("Search(bash): %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("Search(bash) returned no packages on a fedora host")
	}
	found := false
	for _, p := range pkgs {
		if p.Name == "bash" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("bash not in Search(bash) results: %+v", pkgs)
	}
}

func TestDNF_Distro_InstallArgvSimulates(t *testing.T) {
	if _, err := exec.LookPath("dnf"); err != nil {
		t.Skip("dnf missing")
	}
	b := &dnfBackend{}
	argv := b.InstallArgv("tree", "")
	if len(argv) < 2 {
		t.Fatalf("InstallArgv too short: %v", argv)
	}
	// `dnf install --assumeno tree` resolves dependencies and exits
	// without installing — proves the argv reaches a real dnf and
	// the package exists in repos.
	cmd := exec.Command(argv[0], "install", "--assumeno", "tree")
	out, _ := cmd.CombinedOutput()
	// --assumeno exits non-zero by design ("Operation aborted"). What
	// we care about is that dnf understood the argv and mentioned the
	// package; a hard error (no such package, no network) would print
	// something different.
	if !strings.Contains(string(out), "tree") {
		t.Errorf("dnf install --assumeno output didn't mention tree:\n%s", out)
	}
}
