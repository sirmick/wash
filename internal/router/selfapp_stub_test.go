//go:build !multicall

package router

import "testing"

// In standalone builds selfApp is a hard-coded nil return. Any path,
// any context, never matches anything. Guarded by the build tag so a
// future change to selfapp_stub.go that accidentally adds matching
// behavior is caught at test time.
func TestSelfApp_StubAlwaysNil(t *testing.T) {
	for _, p := range []string{
		"",
		"/usr/local/bin/wash",
		"/proc/self/exe",
		"/tmp/wash-fake",
	} {
		if got := selfApp(p); got != nil {
			t.Fatalf("stub selfApp(%q) = %v, want nil", p, got)
		}
	}
}
