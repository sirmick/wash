// Package backend defines the Applier seam (docs/NET.md §4.4, §5): the interface
// the txn engine drives to render and apply a Config, gated by the backend's
// Capabilities. Concrete impls (uci, networkd, nm, and the direct
// netlink/nftables/dnsmasq/hostapd/wireguard backends) live in washnetd and are
// integration-tested in microvms; the engine and its tests use a fake Applier.
package backend

import (
	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

// Artifacts is the rendered, backend-native form of a Config (e.g. UCI text per
// package, or .network files), keyed by an opaque name.
type Artifacts map[string]string

// RollbackToken identifies an applied-but-unconfirmed change so it can be
// confirmed or reverted.
type RollbackToken string

// RenderPlan is the ordered work an Apply will perform to reach Target.
type RenderPlan struct {
	Target model.Config
	Steps  []string
}

// Detection is a backend's cheap, read-only verdict on whether it can run on
// this box (Available) and whether it is the active link manager right now
// (Active). It feeds netd's `auto` backend selection (docs/NET.md §2.7): an
// Active backend means "this is what the box runs" — pick it; Available-only is
// the fallback. Note is a short human reason for the status panel.
type Detection struct {
	Available bool
	Active    bool
	Note      string
}

// Applier renders and applies a Config. Apply must snapshot prior live state so
// Rollback can restore it (the commit-confirm safety property lives here and in
// the txn engine). Verify reads back actual state and returns an error if the
// applied config did not take or broke connectivity — the auto-revert trigger.
type Applier interface {
	Capabilities() caps.Capabilities
	Render(model.Config) (Artifacts, error)
	Apply(RenderPlan) (RollbackToken, error)
	Verify(model.Config) error
	Confirm(RollbackToken) error
	Rollback(RollbackToken) error
}
