//go:build distro_integration

package services

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Runs against the host's real systemctl + /run/systemd/system.
// Skips on hosts that lack either (alpine, non-systemd containers).
//
// "Known-safe" choice: List() reads the unit catalog — no state
// changes. ActionArgv is asserted as a string, never executed.

func TestSystemd_Distro_DetectsAndLists(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl missing")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("not running systemd as PID 1")
	}

	b := Detect()
	if b == nil {
		t.Fatalf("Detect returned nil on a systemd host")
	}
	if b.Name() != "systemd" {
		t.Fatalf("Detect chose %q, want systemd", b.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	services, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(services) == 0 {
		t.Fatalf("List returned empty on a systemd host (expected dozens of units)")
	}
	// systemd's basic.target / sysinit.target / similar core units
	// should always be present. We don't pin a specific one — just
	// require *something* loaded.
	loaded := 0
	for _, s := range services {
		if s.Load == "loaded" {
			loaded++
		}
	}
	if loaded == 0 {
		t.Errorf("no loaded units in List() — looks like the catalog is broken")
	}
}
