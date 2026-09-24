package agentpolicy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// The key store holds the secrets connections name (Connection.Key), such as
// an OpenRouter API key. It is its own file beside agents.json, not a field
// in it, so agents.json stays something a person can share, and it is
// written 0600 because it is the one file here that is a credential. There
// is no keychain behind it yet.
//
// Nothing that reads it sends a value anywhere but into the environment of
// an adapter that uses that connection: not to a window, and not to a log.

// KeysPath is ~/.config/wash/keys.json, beside Path().
func KeysPath() string {
	p := Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "keys.json")
}

// ValidKeyName is the shape of a key's name: short, lower case, no path.
func ValidKeyName(name string) bool {
	if name == "" || len(name) > 40 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// LoadKeys reads the key store. Missing or unreadable is empty: a connection
// that needs a key then reports it unset, which is the truth as far as any
// launch could tell.
func LoadKeys(path string) map[string]string {
	keys := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return keys
	}
	_ = json.Unmarshal(data, &keys)
	return keys
}

// SetKey stores one key, or deletes it when value is empty. Written like
// Save: a temp file in the same directory, chmod 0600 before the rename, so
// the secret is never readable by anyone else, not even for a moment.
func SetKey(path, name, value string) error {
	if !ValidKeyName(name) {
		return errors.New("invalid key name")
	}
	keys := LoadKeys(path)
	if value == "" {
		delete(keys, name)
	} else {
		keys[name] = value
	}
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keys-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// KeyHint is how a stored key is shown once saved: its last four characters,
// never more, and nothing for a key too short to spare them.
func KeyHint(value string) string {
	if len(value) < 12 {
		return ""
	}
	return value[len(value)-4:]
}
