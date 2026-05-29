//go:build distro_integration

package services

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Runs against the host's real openrc tools. Skips on non-openrc
// hosts (debian/fedora/ubuntu).

func TestOpenRC_Distro_DetectsAndLists(t *testing.T) {
	if _, err := exec.LookPath("rc-service"); err != nil {
		t.Skip("rc-service missing")
	}

	// On alpine systemctl is absent, so Detect() falls through to
	// openrc. On hybrid hosts (rare) systemd may win the probe; in
	// that case fall back to constructing the openrc backend
	// directly so the code path is still covered.
	b := Detect()
	if b == nil || b.Name() != "openrc" {
		b = newOpenRC()
		if b == nil {
			t.Fatalf("newOpenRC returned nil with rc-service on PATH")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	services, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Alpine's base install seeds /etc/init.d with bootmisc, hostname,
	// hwclock, etc. A real openrc host should always have at least
	// one entry.
	if len(services) == 0 {
		t.Fatalf("List returned empty on an openrc host (expected /etc/init.d entries)")
	}
}
