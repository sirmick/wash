package priv

import "strings"

// Environment-list helpers shared across the priv backend. An env is the
// usual []string of "KEY=value" entries; entries without a '=' are passed
// through untouched.

// mergeEnv returns base with overlay applied: each base entry whose key is
// in overlay takes the overlay value, and any overlay key not already
// present is appended (in map-iteration order). Base order is preserved.
// A nil/empty overlay returns base unchanged.
//
// This is the single env-overlay primitive; it replaced the
// byte-for-byte-identical mergeEnv (inline.go) and overrideEnv (queue.go).
func mergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overlay))
	seen := make(map[string]bool, len(overlay))
	for _, kv := range base {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:idx]
		if v, ok := overlay[key]; ok {
			out = append(out, key+"="+v)
			seen[key] = true
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overlay {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// filterEnv returns env with every entry whose key is in excluded removed.
func filterEnv(env []string, excluded ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			out = append(out, kv)
			continue
		}
		key := kv[:idx]
		skip := false
		for _, ex := range excluded {
			if key == ex {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}
