// Command washnet-apply renders the given UCI config and applies it to the live
// box — the "apply" step of the read → edit → apply CLI loop. The backend is
// WASH_NETD_BACKEND or, unset, autodetected; the apply is commit-confirmed
// (apply → verify → confirm, with an auto-revert if the change severs the box's
// own default route — the lock-out protection).
//
//	washnet-apply net.uci            # one multi-package buffer (washnet-read output)
//	washnet-apply network.uci ...    # legacy: one raw .uci per package (stem = pkg)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/apps/netd/be/backendsel"
	"github.com/sirmick/wash/cmd/internal/ucibuf"
	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/validate"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: washnet-apply <file.uci> [file.uci ...]")
		os.Exit(2)
	}

	pkgs := map[string]string{}
	for _, p := range os.Args[1:] {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "washnet-apply:", err)
			os.Exit(1)
		}
		if ucibuf.HasMarkers(string(b)) {
			for k, v := range ucibuf.Unmarshal(string(b)) {
				pkgs[k] = v
			}
		} else { // legacy: a raw single-package file, keyed by filename stem
			pkgs[strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))] = string(b)
		}
	}

	cfg, err := codec.Parse(pkgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-apply: parse uci:", err)
		os.Exit(1)
	}

	name := os.Getenv("WASH_NETD_BACKEND")
	if name == "" {
		name, _ = backendsel.Autodetect()
	}
	a := backendsel.New(name)
	if a == nil {
		fmt.Fprintf(os.Stderr, "washnet-apply: no live backend for %q\n", name)
		os.Exit(1)
	}

	// Capability check: reject what this backend can't express (the CLI form of
	// the UI's grey-out).
	for _, d := range validate.Validate(cfg, a.Capabilities()) {
		if d.Severity == validate.Error {
			fmt.Fprintf(os.Stderr, "washnet-apply: error: %s\n", d.Message)
			os.Exit(1)
		}
	}

	token, err := a.Apply(backend.RenderPlan{Target: cfg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "washnet-apply: APPLY FAILED: %v\n", err)
		os.Exit(1)
	}
	if err := a.Verify(cfg); err != nil {
		_ = a.Rollback(token)
		fmt.Fprintf(os.Stderr, "washnet-apply: REVERTED (verify failed): %v\n", err)
		os.Exit(1)
	}
	if err := a.Confirm(token); err != nil {
		fmt.Fprintf(os.Stderr, "washnet-apply: confirm: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("APPLIED (%s)\n", name)
}
