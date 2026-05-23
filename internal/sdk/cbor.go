package sdk

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// CBOR-decoded values arrive at OnAppMsg / OnAppMsgFrom with shapes
// that depend on what crossed the wire: integers can land as int64,
// uint64, or float64 (CBOR preserves type; JSON-via-CBOR collapses to
// float64); maps land as map[any]any rather than map[string]any.
// These helpers do the per-field coercion every app needs and used
// to redefine for itself.
//
// The shape they assume is whatever cbor.Unmarshal(payload, &m)
// produced, where m is any. Apps that genuinely care about precise
// types can drop down to cbor.Unmarshal with a typed struct.

// AsMap unwraps a CBOR-decoded map[any]any (or already-string-keyed
// map[string]any) into map[string]any. Non-string keys are dropped.
// Returns nil if data isn't a map.
func AsMap(data any) map[string]any {
	switch x := data.(type) {
	case map[string]any:
		return x
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			if ks, ok := k.(string); ok {
				out[ks] = v
			}
		}
		return out
	}
	return nil
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

// ToInt64 coerces CBOR's mixed numeric shapes into int64.
func ToInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint32:
		return int64(x)
	}
	return 0
}

// ToUint64 coerces CBOR's mixed numeric shapes into uint64.
// Negative int64 values clamp to 0.
func ToUint64(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case float64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case uint32:
		return uint64(x)
	case int:
		if x < 0 {
			return 0
		}
		return uint64(x)
	}
	return 0
}

// ToUint32 coerces CBOR's mixed numeric shapes into uint32. Also
// accepts strings (decimal or with leading 0 / 0o / 0x base markers)
// so an FE that types "0755" verbatim works for chmod-style fields.
// Returns (0, false) when v can't be parsed.
func ToUint32(v any) (uint32, bool) {
	switch x := v.(type) {
	case uint64:
		return uint32(x), true
	case int64:
		return uint32(x), true
	case float64:
		return uint32(x), true
	case uint32:
		return x, true
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

// ToStringSlice coerces a CBOR-decoded array-of-strings ([]any with
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

// ToStringMap coerces a CBOR-decoded map-of-strings into
// map[string]string. Accepts map[any]any (CBOR's typical decode),
// map[string]any, and map[string]string. Mixed types yield only the
// string→string entries.
func ToStringMap(v any) map[string]string {
	out := map[string]string{}
	switch x := v.(type) {
	case map[any]any:
		for k, vv := range x {
			ks, _ := k.(string)
			vs, _ := vv.(string)
			if ks != "" {
				out[ks] = vs
			}
		}
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

// stringify is a fallback for non-string CBOR map keys when AsMap
// would otherwise drop them. Used by ToJSONValue.
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
