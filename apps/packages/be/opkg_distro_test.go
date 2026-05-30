//go:build distro_integration

package packages

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Runs against the host's real opkg. Skips if opkg is missing. Inside
// the matrix's openwrt container it runs unconditionally.

func TestOPKG_Distro_DetectsAndSearchesBusybox(t *testing.T) {
	if _, err := exec.LookPath("opkg"); err != nil {
		t.Skip("opkg missing")
	}
	b := Detect()
	if b == nil {
		t.Fatalf("Detect returned nil with opkg present")
	}
	if b.Name() != "opkg" {
		// apt/dnf/apk won the probe — exercise opkg directly.
		b = newOPKG()
		if b == nil {
			t.Fatalf("newOPKG returned nil with opkg on PATH")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// busybox is universally present on OpenWRT.
	pkgs, err := b.Search(ctx, "busybox")
	if err != nil {
		t.Fatalf("Search(busybox): %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("Search(busybox) returned no packages on an openwrt host")
	}
	found := false
	for _, p := range pkgs {
		if p.Name == "busybox" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("busybox not in Search(busybox) results: %+v", pkgs)
	}
}
