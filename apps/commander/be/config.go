package commander

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Settings is what the person controls (docs/COMMANDER.md §5.3): whether
// the commander runs at all, whether it may use a hosted provider, and
// how hard it works. Persisted at $XDG_CONFIG_HOME/wash/commander.json.
type Settings struct {
	// Automatic turns the schedule on. Off by default: nothing is observed
	// or sent anywhere until a person switches it on.
	Automatic bool `json:"automatic"`
	// Hosted allows automatic briefs through a provider that is not on
	// this box. Its own switch, its own warning.
	Hosted bool `json:"hosted"`
	// IntervalSec is the cadence while something changes; the schedule
	// backs off to 4× when nothing does.
	IntervalSec int `json:"interval_sec"`
	// BudgetPerHour caps model requests per rolling hour.
	BudgetPerHour int `json:"budget_per_hour"`
	// BatchMax is how many changed windows one request briefs.
	BatchMax int `json:"batch_max"`
}

const (
	defaultInterval = 300
	minInterval     = 5
	defaultBudget   = 40
	defaultBatch    = 6
	maxBatch        = 12
	// batchBytes bounds one request's content.
	batchBytes = 96 << 10
)

func defaultSettings() Settings {
	return Settings{IntervalSec: defaultInterval, BudgetPerHour: defaultBudget, BatchMax: defaultBatch}
}

// normalised fills zeroes with defaults and clamps what a hand-edited
// file could set to something the schedule cannot honour.
func (s Settings) normalised() Settings {
	if s.IntervalSec <= 0 {
		s.IntervalSec = defaultInterval
	}
	if s.IntervalSec < minInterval {
		s.IntervalSec = minInterval
	}
	if s.BudgetPerHour <= 0 {
		s.BudgetPerHour = defaultBudget
	}
	if s.BatchMax <= 0 {
		s.BatchMax = defaultBatch
	}
	if s.BatchMax > maxBatch {
		s.BatchMax = maxBatch
	}
	return s
}

type configFile struct {
	Version int `json:"version"`
	Settings
}

func configPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash", "commander.json")
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".config", "wash", "commander.json")
	}
	return ""
}

func loadSettings() (Settings, error) {
	p := configPath()
	if p == "" {
		return defaultSettings(), nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return defaultSettings(), nil
	}
	if err != nil {
		return defaultSettings(), err
	}
	var f configFile
	if err := json.Unmarshal(b, &f); err != nil {
		return defaultSettings(), err
	}
	if f.Version != 1 {
		return defaultSettings(), errors.New("unsupported commander config version")
	}
	return f.Settings.normalised(), nil
}

func saveSettings(s Settings) error {
	p := configPath()
	if p == "" {
		return errors.New("no config dir")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(configFile{Version: 1, Settings: s}, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
