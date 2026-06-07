package disks

import (
	"context"
	"os"
	"os/exec"
	"strings"
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

// Provider is one logical-storage manager (md, lvm, btrfs, zfs). The base
// interface is just identity + detection; how a provider is collected depends
// on its tier — PollProvider (unprivileged) or ShellProvider (privileged).
type Provider interface {
	// Name is the Manager.Kind this provider populates.
	Name() string
	// Detect reports whether this manager is present/relevant on the host
	// (drives the capabilities flags). Cheap; no privileged calls.
	Detect() bool
	// Privileged reports whether collection needs root.
	Privileged() bool
}

// PollProvider is an unprivileged provider collected every poll tick (md reads
// /proc/mdstat + sysfs in Go). present=false means "detected, nothing to show".
type PollProvider interface {
	Collect(ctx context.Context) (mgr Manager, present bool, err error)
}

// ShellProvider is a privileged provider whose data comes from running shell
// commands. Implementing it lets scanPrivileged fold every privileged provider
// into ONE escalation (one wash-priv approval) instead of one sudo prompt each:
// their scripts are concatenated behind per-provider markers, run in a single
// `sh -c`, then each section is handed back to its parser. lvm/btrfs/zfs
// implement this; md is unprivileged and uses Collect in the poll.
type ShellProvider interface {
	// ScanScript returns the shell commands that emit this provider's data, and
	// whether there's anything to run (e.g. btrfs needs a mounted fs).
	ScanScript(ctx context.Context) (script string, run bool)
	// ParseScan turns this provider's section of the combined output into a
	// Manager. present=false means "detected but nothing to show".
	ParseScan(out string) (mgr Manager, present bool, err error)
}

// providerMarker fences each provider's section in the combined scan output.
// Distinct from the providers' own internal markers (btrfs's @@SUBVOL/@@STATS,
// zfs's @@STATUS/@@DATASETS) so those survive inside a section.
const providerMarker = "@@WASHPROVIDER:"

// providers is the registered set, appended by each provider file's init().
// M1 registers only md; M3–M5 add lvm/btrfs/zfs.
var providers []Provider

func registerProvider(p Provider) { providers = append(providers, p) }

// scanPrivileged collects every detected privileged provider in ONE escalation:
// one combined `sh -c` (→ a single wash-priv approval / sudo invocation), then
// splits the output per provider and parses each. run==nil → nothing (the poll
// never escalates).
func scanPrivileged(ctx context.Context, run RunFunc) []Manager {
	if run == nil {
		return nil
	}
	var sb strings.Builder
	type entry struct {
		name string
		sp   ShellProvider
	}
	var entries []entry
	for _, p := range providers {
		sp, ok := p.(ShellProvider)
		if !ok || !p.Privileged() || !p.Detect() {
			continue
		}
		script, runIt := sp.ScanScript(ctx)
		if !runIt {
			continue
		}
		sb.WriteString("echo '" + providerMarker + p.Name() + "'\n")
		sb.WriteString(script + "\n")
		entries = append(entries, entry{p.Name(), sp})
	}
	if len(entries) == 0 {
		return nil
	}
	out, err := run(ctx, []string{"sh", "-c", sb.String()}, "scan storage volumes (LVM / btrfs / ZFS)")
	if err != nil {
		return nil
	}
	sections := splitProviderSections(string(out))
	var mgrs []Manager
	for _, e := range entries {
		mgr, present, err := e.sp.ParseScan(sections[e.name])
		if err == nil && present {
			mgrs = append(mgrs, mgr)
		}
	}
	return mgrs
}

// splitProviderSections splits combined scan output into per-provider bodies,
// keyed by name, splitting only on providerMarker lines (provider-internal
// @@ markers stay within their section).
func splitProviderSections(out string) map[string]string {
	res := map[string]string{}
	cur := ""
	var b strings.Builder
	flush := func() {
		if cur != "" {
			res[cur] = b.String()
			b.Reset()
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, providerMarker); ok {
			flush()
			cur = strings.TrimSpace(name)
			continue
		}
		if cur != "" {
			b.WriteString(line + "\n")
		}
	}
	flush()
	return res
}

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
		if !detected || p.Privileged() {
			continue // privileged providers are collected together below
		}
		if pp, ok := p.(PollProvider); ok {
			if mgr, present, err := pp.Collect(ctx); err == nil && present {
				snap.Managers = append(snap.Managers, mgr)
			}
		}
	}
	// All privileged providers in one escalation (one priv approval / sudo),
	// only when a runner is supplied (the GUI scan or root --dump-snapshot).
	snap.Managers = append(snap.Managers, scanPrivileged(ctx, run)...)

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
