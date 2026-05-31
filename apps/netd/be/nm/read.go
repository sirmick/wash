package nm

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/nmprofile"
)

// ReadSystemConnections loads the box's current networking into the model by
// reading NM's saved keyfiles and decompiling them (ParseKeyfiles) — the read
// path the UI uses to show current settings, and the `base` the commit-confirm
// engine diffs an edit against. We map only the fields the model represents;
// anything NM added (uuid, timestamps, settings we don't model) is ignored on
// read and preserved on write by editing the existing connection in place.
func ReadSystemConnections() (model.Config, error) { return readDir(SystemConnDir) }

// readDir is ReadSystemConnections against an explicit dir (overridable for tests).
func readDir(dir string) (model.Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return model.Config{}, nil
		}
		return model.Config{}, err
	}
	kfs := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".nmconnection") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return model.Config{}, err
		}
		// Key by filename stem; the real id is read from [connection] id inside.
		kfs[strings.TrimSuffix(e.Name(), ".nmconnection")] = string(b)
	}
	return nmprofile.ParseKeyfiles(kfs)
}
