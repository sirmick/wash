package inferenceapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRoundTripIsPrivate(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	cfg := defaultConfig()
	cfg.Default = "openai"
	cfg.Connections[1].Model = "small-model"
	cfg.Connections[1].APIKey = "secret-value"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "wash", "inference.json")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%o, want 600", got)
	}
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != "openai" || got.Connections[1].APIKey != "secret-value" {
		t.Fatalf("round trip lost config: %#v", got)
	}
	s := (&server{cfg: got}).publicStateLocked()
	for _, c := range s.Connections {
		if strings.Contains(c.Detail, "secret-value") {
			t.Fatal("public state exposed API key")
		}
	}
	if !s.Connections[1].HasCredential {
		t.Fatal("redacted state should report credential presence")
	}
}

func TestLoadMissingConfigDefaultsOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Default != "" {
		t.Fatalf("default=%q; AI must be opt-in", cfg.Default)
	}
	if len(cfg.Connections) != 4 {
		t.Fatalf("connections=%d, want 4", len(cfg.Connections))
	}
}

// The on-box rule the commander's automatic mode relies on: CLI adapters
// and loopback endpoints are local, anything else is hosted.
func TestIsLocal(t *testing.T) {
	cases := map[connection]bool{
		{Adapter: "codex"}: true,
		{Adapter: "claude", BaseURL: "https://api.anthropic.com"}:  true,
		{Adapter: "openai", BaseURL: "http://127.0.0.1:11434/v1"}:  true,
		{Adapter: "openai", BaseURL: "http://localhost:8080/v1"}:   true,
		{Adapter: "openai", BaseURL: "http://[::1]:8080/v1"}:       true,
		{Adapter: "openai", BaseURL: "https://api.openai.com/v1"}:  false,
		{Adapter: "openai", BaseURL: "http://ollama.lan:11434/v1"}: false,
		{Adapter: "openai", BaseURL: ""}:                           false,
	}
	for c, want := range cases {
		if got := isLocal(c); got != want {
			t.Errorf("isLocal(%+v) = %v, want %v", c, got, want)
		}
	}
}
