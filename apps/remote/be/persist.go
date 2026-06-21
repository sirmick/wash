package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Persisted mounts back the "reconnect at launch" option: a user who ticks it
// gets the mount re-established when com.wash.remote next starts (login / reboot),
// the way wash-connect persists saved hosts. Stored under
// $XDG_CONFIG_HOME/wash/mounts.json (atomic temp+rename), keyed by host+root.

type savedMount struct {
	Host       string `json:"host"`
	RemoteRoot string `json:"remote_root"`
}

var savedMountsMu sync.Mutex

// configDir mirrors wash-connect/wash-settings' resolver. Local because the
// contract is the path; a single-consumer helper belongs in its consumer.
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash")
	}
	return ""
}

func mountsFile() string {
	if dir := configDir(); dir != "" {
		return filepath.Join(dir, "mounts.json")
	}
	return ""
}

// loadSavedMounts returns the persisted mounts (empty on any error — a missing
// or corrupt file just means "nothing to restore").
func loadSavedMounts() []savedMount {
	savedMountsMu.Lock()
	defer savedMountsMu.Unlock()
	return readMountsLocked()
}

func readMountsLocked() []savedMount {
	path := mountsFile()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ms []savedMount
	_ = json.Unmarshal(data, &ms)
	return ms
}

func writeMountsLocked(ms []savedMount) {
	path := mountsFile()
	if path == "" {
		return
	}
	out, err := json.MarshalIndent(ms, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".mounts-*.json.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpPath, path)
}

// addSavedMount records host:remoteRoot for re-mount at launch (idempotent).
func addSavedMount(host, remoteRoot string) {
	savedMountsMu.Lock()
	defer savedMountsMu.Unlock()
	ms := readMountsLocked()
	for _, m := range ms {
		if m.Host == host && m.RemoteRoot == remoteRoot {
			return
		}
	}
	writeMountsLocked(append(ms, savedMount{Host: host, RemoteRoot: remoteRoot}))
}

// removeSavedMount forgets a persisted mount (called on unmount).
func removeSavedMount(host, remoteRoot string) {
	savedMountsMu.Lock()
	defer savedMountsMu.Unlock()
	ms := readMountsLocked()
	out := ms[:0]
	for _, m := range ms {
		if m.Host != host || m.RemoteRoot != remoteRoot {
			out = append(out, m)
		}
	}
	writeMountsLocked(out)
}
