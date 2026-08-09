package sdk

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// Helpers for poking into JSON-decoded `any` values returned from
// OnAppMsg / OnAppMsgFrom. encoding/json decodes JSON objects into
// map[string]any, numbers into float64, booleans into bool, strings
// into string, and arrays into []any. These helpers do the per-field
// coercion that would otherwise be repeated in every app.

// AsMap unwraps a JSON-decoded map into map[string]any. Returns nil
// if data isn't a map. The event channel is JSON, so the data arrives
// already-string-keyed — AsMap is a typed shortcut for the
// `data.(map[string]any)` assertion plus a nil-safe fallback.
func AsMap(data any) map[string]any {
	m, _ := data.(map[string]any)
	return m
}

// ToString returns v as a string, or "" if v is missing or wrong-typed.
func ToString(v any) string {
	s, _ := v.(string)
	return s
}

// ToBool returns v as a bool, or false if v is missing or wrong-typed.
func ToBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// ToInt64 coerces a JSON-decoded numeric value into int64. JSON
// numbers land as float64 when the target is `any`; we also accept
// the typed-int forms in case a struct-typed decoder filled the map.
func ToInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

// ToUint64 coerces a JSON-decoded numeric value into uint64.
// Negative values clamp to 0.
func ToUint64(v any) uint64 {
	switch x := v.(type) {
	case float64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case uint64:
		return x
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case int:
		if x < 0 {
			return 0
		}
		return uint64(x)
	}
	return 0
}

// ToUint32 coerces a JSON-decoded numeric value into uint32. Also
// accepts strings (decimal or with leading 0 / 0o / 0x base markers)
// so an FE that types "0755" verbatim works for chmod-style fields.
// Returns (0, false) when v can't be parsed.
func ToUint32(v any) (uint32, bool) {
	switch x := v.(type) {
	case float64:
		return uint32(x), true
	case uint64:
		return uint32(x), true
	case int64:
		return uint32(x), true
	case int:
		return uint32(x), true
	case string:
		s := strings.TrimPrefix(x, "0o")
		n, err := strconv.ParseUint(s, 0, 32)
		if err != nil {
			n, err = strconv.ParseUint(x, 10, 32)
			if err != nil {
				return 0, false
			}
		}
		return uint32(n), true
	}
	return 0, false
}

// ToStringSlice coerces a JSON-decoded array-of-strings ([]any with
// string elements, or already-typed []string) into []string.
// Non-string elements are dropped silently.
func ToStringSlice(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}

// ToStringMap coerces a JSON-decoded map-of-strings into
// map[string]string. Accepts map[string]any (JSON's default) and
// map[string]string. Mixed types yield only the string→string entries.
func ToStringMap(v any) map[string]string {
	out := map[string]string{}
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if s, ok := vv.(string); ok {
				out[k] = s
			}
		}
	case map[string]string:
		for k, v := range x {
			out[k] = v
		}
	}
	return out
}

// DecodeBase64 decodes v as a standard-encoded base64 string. Returns
// nil on missing / wrong-typed / malformed input — apps generally
// treat empty bytes the same as absence.
func DecodeBase64(v any) []byte {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
