package disks

import (
	"context"
	"os"
	"os/exec"
)

// RunFunc runs a privileged command and returns its stdout. Two
// implementations exist:
//
//   - directRunner: exec.Command, used by --dump-snapshot when already root.
//   - the wash-priv runner (M2+): conn.PrivRunInlineSync, used by on-demand
//     FE requests so the user approves the escalation.
//
// A nil RunFunc means "no privileged access available" — privileged
// providers are skipped (their objects omitted) but still detected for the
// capabilities flags. The always-on poll passes nil; only explicit user
// actions get a real runner.
type RunFunc func(ctx context.Context, argv []string, reason string) ([]byte, error)

// directRunner shells out directly. Only meaningful when the process is
// root (the --dump-snapshot path in the VM tests); otherwise the underlying
// tool fails with EACCES and the provider drops the object.
func directRunner(ctx context.Context, argv []string, _ string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, os.ErrInvalid
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}

// Provider is one logical-storage manager (md, lvm, btrfs, zfs).
type Provider interface {
	// Name is the Manager.Kind this provider populates.
	Name() string
	// Detect reports whether this manager is present/relevant on the host
	// (drives the capabilities flags). Cheap; no privileged calls.
	Detect() bool
	// Privileged reports whether Collect needs root. Privileged providers
	// are only collected when a non-nil RunFunc is supplied.
	Privileged() bool
	// Collect gathers the manager's current objects. present=false means
	// "detected but nothing to show" (e.g. tool installed, no arrays).
	Collect(ctx context.Context, run RunFunc) (mgr Manager, present bool, err error)
}

// providers is the registered set, appended by each provider file's init().
// M1 registers only md; M3–M5 add lvm/btrfs/zfs.
var providers []Provider

func registerProvider(p Provider) { providers = append(providers, p) }

// collectSnapshot assembles a full snapshot: the unprivileged physical layer
// plus every detected manager. Privileged providers are collected only when
// run != nil.
func collectSnapshot(ctx context.Context, run RunFunc) Snapshot {
	if os.Getenv("WASH_DISKS_SOURCE") == "fake" {
		return fakeSnapshot()
	}

	snap := Snapshot{
		TS:          nowUnix(),
		Disks:       collectDisks(),
		Filesystems: collectFilesystems(),
	}

	for _, p := range providers {
		detected := p.Detect()
		setCapability(&snap.Capabilities, p.Name(), detected)
		if !detected {
			continue
		}
		if p.Privileged() && run == nil {
			continue // poll loop / unprivileged dump: detection only
		}
		mgr, present, err := p.Collect(ctx, run)
		if err != nil || !present {
			continue
		}
		snap.Managers = append(snap.Managers, mgr)
	}

	// SMART is per-disk and privileged; we only advertise its availability
	// here (the actual probe is an on-demand FE request, M2).
	snap.Capabilities.SMART = smartAvailable()
	return snap
}

func setCapability(c *Capabilities, name string, v bool) {
	switch name {
	case "md":
		c.MD = v
	case "lvm":
		c.LVM = v
	case "btrfs":
		c.Btrfs = v
	case "zfs":
		c.ZFS = v
	}
}

// smartAvailable reports whether smartctl is on PATH (the M2 probe tool).
func smartAvailable() bool {
	_, err := exec.LookPath("smartctl")
	return err == nil
}

// toolOnPath is a small helper for providers' Detect().
func toolOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
