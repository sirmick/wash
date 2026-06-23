package router

import "testing"

func TestRequireLoopbackTCP(t *testing.T) {
	ok := []string{"127.0.0.1:5000", "localhost:5000", "[::1]:5000", "127.10.20.30:80"}
	for _, addr := range ok {
		if err := requireLoopbackTCP(addr); err != nil {
			t.Errorf("requireLoopbackTCP(%q) = %v, want nil", addr, err)
		}
	}
	bad := []string{
		"10.0.0.5:5432",   // routable LAN
		"0.0.0.0:5000",    // wildcard
		"example.com:443", // hostname (no DNS at publish)
		"127.0.0.1",       // missing port
		"",                // empty
	}
	for _, addr := range bad {
		if err := requireLoopbackTCP(addr); err == nil {
			t.Errorf("requireLoopbackTCP(%q) = nil, want error", addr)
		}
	}
}
