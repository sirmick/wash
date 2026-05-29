//go:build distro_integration

package packages

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Runs against the host's real apt-get + apt-cache + dpkg-query.
// Skips if any of them is missing. Inside the matrix's debian/ubuntu
// containers it should always run.
//
// "Known-safe" choice: `apt-cache search bash` reads the package
// index — no installs, no network beyond what's already cached.
// `apt-get -s install tree` (simulate-only) proves the action argv
// shells out cleanly to a real apt-get without touching state.

func TestAPT_Distro_DetectsAndSearchesBash(t *testing.T) {
	for _, bin := range []string{"apt-get", "apt-cache", "dpkg-query"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("missing %s: %v", bin, err)
		}
	}
	b := Detect()
	if b == nil {
		t.Fatalf("Detect returned nil on a host with apt-get installed")
	}
	if b.Name() != "apt" {
		t.Fatalf("Detect chose %q, want apt", b.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pkgs, err := b.Search(ctx, "bash")
	if err != nil {
		t.Fatalf("Search(bash): %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("Search(bash) returned no packages on a debian/ubuntu host")
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

func TestAPT_Distro_InstallArgvSimulates(t *testing.T) {
	if _, err := exec.LookPath("apt-get"); err != nil {
		t.Skip("apt-get missing")
	}
	b := &aptBackend{}
	argv := b.InstallArgv("tree", "")
	if len(argv) < 2 {
		t.Fatalf("InstallArgv too short: %v", argv)
	}
	// `apt-get -s install tree` is simulate-only — no state touched,
	// no root required. Validates the argv invocation against a real
	// apt-get binary.
	cmd := exec.Command(argv[0], append([]string{"-s"}, argv[1:]...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apt-get -s install tree failed: %v\noutput:\n%s", err, out)
	}
	// The simulator names the package somewhere in its output.
	if !strings.Contains(string(out), "tree") {
		t.Errorf("apt-get -s output didn't mention tree:\n%s", out)
	}
}
