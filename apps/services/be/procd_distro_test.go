//go:build distro_integration

package services

import (
	"context"
	"os"
	"testing"
	"time"
)

// Runs against the host's real /sbin/procd + /etc/init.d. Skips on
// non-openwrt hosts. Inside the matrix's openwrt container it runs
// unconditionally — note that ubus has no daemon to talk to in the
// container (procd isn't PID 1), so List returns the catalog with
// Active="inactive" for everything. That's correct behaviour in the
// container; full coverage of the ubus-backed running-state path
// would need a real PID-1 procd to exercise.

func TestProcd_Distro_DetectsAndLists(t *testing.T) {
	if _, err := os.Stat("/sbin/procd"); err != nil {
		t.Skip("/sbin/procd missing — not an openwrt host")
	}
	if _, err := os.Stat("/etc/init.d"); err != nil {
		t.Skip("/etc/init.d missing")
	}

	b := Detect()
	if b == nil || b.Name() != "procd" {
		b = newProcd()
		if b == nil {
			t.Fatalf("newProcd returned nil with /sbin/procd present")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	services, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(services) == 0 {
		t.Fatalf("List returned empty on an openwrt host (expected /etc/init.d entries)")
	}
}
