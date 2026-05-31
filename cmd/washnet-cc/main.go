// Command washnet-cc drives the commit-confirm engine against live
// NetworkManager for the lock-out truth test (docs/NET.md §7, §10). It first
// establishes a default route with <establish.uci>, then runs the txn engine on
// <break.uci> — which APPLYs, VERIFYs (default-route regression), and, when the
// change severs the box's own way out, auto-REVERTs. Prints the outcome so the
// in-VM test can assert it over the ctl plane. Both inputs are network-package
// UCI (eth0 reconfig).
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sirmick/wash/apps/netd/be/nm"
	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/txn"
)

func parse(path string) model.Config {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-cc:", err)
		os.Exit(1)
	}
	cfg, err := codec.Parse(map[string]string{"network": string(b)})
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-cc: parse:", err)
		os.Exit(1)
	}
	return cfg
}

func routeStr() string {
	if nm.HasDefaultRoute() {
		return "default-route=present"
	}
	return "default-route=MISSING"
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: washnet-cc <establish.uci> <break.uci>")
		os.Exit(2)
	}
	estab := parse(os.Args[1])
	brk := parse(os.Args[2])
	a := nm.NewApplier()

	// 1. Establish a default route and confirm it (the baseline revert returns to).
	tok, err := a.Apply(backend.RenderPlan{Target: estab})
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-cc: establish:", err)
		os.Exit(1)
	}
	_ = a.Confirm(tok)
	if !waitRoute(30 * time.Second) {
		fmt.Println("SKIP: never got a default route (no DHCP/internet in this env)")
		return
	}
	fmt.Println("ESTABLISHED", routeStr())

	// 2. Commit-confirm the WAN-breaker. txn.Apply renders→applies→verifies and,
	//    on the default-route-regression VERIFY failure, auto-reverts to base.
	job, err := txn.Apply(a.Live(), brk, a)
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-cc: apply:", err)
		os.Exit(1)
	}
	fmt.Printf("STATE %s\n", job.State())
	// The revert reactivates the baseline (DHCP), whose lease/route takes a beat
	// to re-acquire — wait for it before reporting.
	waitRoute(15 * time.Second)
	fmt.Printf("after %s\n", routeStr())
}

func waitRoute(d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); {
		if nm.HasDefaultRoute() {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}
