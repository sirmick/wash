//go:build distro_integration

package packages

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Runs against the host's real apk. Skips if apk is missing. Inside
// the matrix's alpine container it should always run.

func TestAPK_Distro_DetectsAndSearchesBash(t *testing.T) {
	if _, err := exec.LookPath("apk"); err != nil {
		t.Skip("apk missing")
	}
	b := Detect()
	if b == nil {
		t.Fatalf("Detect returned nil with apk present")
	}
	if b.Name() != "apk" {
		// apt or dnf won the probe — exercise the apk backend
		// directly so the code path is still covered.
		b = newAPK()
		if b == nil {
			t.Fatalf("newAPK returned nil with apk on PATH")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pkgs, err := b.Search(ctx, "bash")
	if err != nil {
		t.Fatalf("Search(bash): %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("Search(bash) returned no packages on an alpine host")
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

func TestAPK_Distro_InstallArgvSimulates(t *testing.T) {
	if _, err := exec.LookPath("apk"); err != nil {
		t.Skip("apk missing")
	}
	b := &apkBackend{}
	argv := b.InstallArgv("tree", "")
	if len(argv) < 2 {
		t.Fatalf("InstallArgv too short: %v", argv)
	}
	// `apk add --simulate tree` is the apk equivalent of apt-get -s.
	cmd := exec.Command(argv[0], "add", "--simulate", "tree")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apk add --simulate tree failed: %v\noutput:\n%s", err, out)
	}
	_ = out
}
