package inferenceapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type connection struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Adapter string `json:"adapter"` // openai|codex|claude
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

type config struct {
	Version     int          `json:"version"`
	Default     string       `json:"default,omitempty"`
	Connections []connection `json:"connections"`
}

func defaultConfig() config {
	return config{Version: 1, Connections: []connection{
		{ID: "ollama", Name: "Ollama", Adapter: "openai", BaseURL: "http://127.0.0.1:11434/v1"},
		{ID: "openai", Name: "OpenAI-compatible API", Adapter: "openai", BaseURL: "https://api.openai.com/v1"},
		{ID: "codex", Name: "Codex CLI", Adapter: "codex"},
		{ID: "claude", Name: "Claude Code CLI", Adapter: "claude"},
	}}
}

func configPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash", "inference.json")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash", "inference.json")
	}
	return ""
}

func loadConfig() (config, error) {
	cfg := defaultConfig()
	p := configPath()
	if p == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var saved config
	if err := json.Unmarshal(b, &saved); err != nil {
		return cfg, err
	}
	if saved.Version != 1 {
		return cfg, errors.New("unsupported inference config version")
	}
	// Retain built-in connection identities, while accepting their saved fields.
	byID := map[string]connection{}
	for _, v := range saved.Connections {
		byID[v.ID] = v
	}
	for i, v := range cfg.Connections {
		if got, ok := byID[v.ID]; ok {
			// IDs, display names and adapters are code-owned. Only the fields
			// exposed by Settings are accepted from disk, so a hand-edited file
			// cannot turn a built-in connection into another execution mode.
			v.BaseURL, v.Model, v.APIKey = got.BaseURL, got.Model, got.APIKey
			cfg.Connections[i] = v
		}
	}
	cfg.Default = saved.Default
	return cfg, nil
}

func saveConfig(cfg config) error {
	p := configPath()
	if p == "" {
		return errors.New("cannot resolve user config directory")
	}
	d := filepath.Dir(p)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(d, ".inference-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
