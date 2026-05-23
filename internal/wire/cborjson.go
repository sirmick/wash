package wire

import (
	"encoding/base64"
	"fmt"
)

// ToJSONValue walks a CBOR-decoded value and produces an equivalent
// shape that encoding/json will marshal cleanly. Two rewrites:
//
//   - map[any]any (CBOR's normal decode shape) becomes map[string]any
//     so json.Marshal can serialise it. Non-string keys are stringified
//     via fmt.Sprint.
//   - []byte becomes a base64 standard-encoded string so the value
//     round-trips through JSON without the router's CBOR→JSON
//     normaliser doing it implicitly (which would surprise apps that
//     expect raw bytes).
//
// Other scalars and slices pass through unchanged. The function is
// safe to call on already-JSON-shaped values (map[string]any), which
// makes it usable as a uniform "make this json-marshalable" pass.
func ToJSONValue(v any) any {
	switch x := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprint(k)
			}
			out[ks] = ToJSONValue(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = ToJSONValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = ToJSONValue(vv)
		}
		return out
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	}
	return v
}
