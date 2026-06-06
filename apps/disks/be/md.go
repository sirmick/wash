package disks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procMdstat is a package var so tests can point it at a fixture.
var procMdstat = "/proc/mdstat"

// mdProvider reads md (software RAID) arrays from /sys/block/md*/md/ — fully
// structured and unprivileged, so it runs in the always-on poll. /proc/mdstat
// is used only for the cheap presence check.
type mdProvider struct{}

func init() { registerProvider(mdProvider{}) }

func (mdProvider) Name() string     { return "md" }
func (mdProvider) Privileged() bool { return false }

// Detect: any md* device with an md/ control dir under /sys/block, or a
// non-empty /proc/mdstat array list.
func (mdProvider) Detect() bool {
	if entries, err := os.ReadDir(sysBlock); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "md") {
				if _, err := os.Stat(filepath.Join(sysBlock, e.Name(), "md", "level")); err == nil {
					return true
				}
			}
		}
	}
	return false
}

func (mdProvider) Collect(_ context.Context, _ RunFunc) (Manager, bool, error) {
	entries, err := os.ReadDir(sysBlock)
	if err != nil {
		return Manager{}, false, err
	}
	var arrays []any
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "md") {
			continue
		}
		mdDir := filepath.Join(sysBlock, name, "md")
		if _, err := os.Stat(filepath.Join(mdDir, "level")); err != nil {
			continue
		}
		arrays = append(arrays, readMDArray(name, mdDir))
	}
	if len(arrays) == 0 {
		return Manager{}, false, nil
	}
	return Manager{Kind: "md", Objects: arrays}, true, nil
}

func readMDArray(name, mdDir string) MDArray {
	a := MDArray{
		Name:       name,
		Path:       "/dev/" + name,
		Level:      readTrim(filepath.Join(mdDir, "level")),
		State:      readTrim(filepath.Join(mdDir, "array_state")),
		SyncAction: readTrim(filepath.Join(mdDir, "sync_action")),
		Size:       readSectors(filepath.Join(sysBlock, name, "size")),
		Degraded:   readTrim(filepath.Join(mdDir, "degraded")) != "" && readTrim(filepath.Join(mdDir, "degraded")) != "0",
	}
	a.RaidDisks, _ = strconv.Atoi(readTrim(filepath.Join(mdDir, "raid_disks")))
	a.SyncPct = readSyncPct(filepath.Join(mdDir, "sync_completed"))
	a.Members = readMDMembers(mdDir)
	return a
}

// readSyncPct parses sync_completed ("done / total" in sectors) into a
// percentage. "none" / "idle" → 0.
func readSyncPct(path string) float64 {
	s := readTrim(path)
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return 0
	}
	done, err1 := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
	total, err2 := strconv.ParseFloat(strings.TrimSpace(s[i+1:]), 64)
	if err1 != nil || err2 != nil || total <= 0 {
		return 0
	}
	return done / total * 100.0
}

// readMDMembers reads the dev-<name>/ component dirs under md/.
func readMDMembers(mdDir string) []MDMember {
	entries, err := os.ReadDir(mdDir)
	if err != nil {
		return nil
	}
	var members []MDMember
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "dev-") {
			continue
		}
		devDir := filepath.Join(mdDir, e.Name())
		m := MDMember{
			Name:  strings.TrimPrefix(e.Name(), "dev-"),
			State: readTrim(filepath.Join(devDir, "state")),
			Slot:  -1,
		}
		if slot := readTrim(filepath.Join(devDir, "slot")); slot != "" && slot != "none" {
			if v, err := strconv.Atoi(slot); err == nil {
				m.Slot = v
			}
		}
		members = append(members, m)
	}
	return members
}
