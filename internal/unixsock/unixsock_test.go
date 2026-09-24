package unixsock

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Two listeners for the same app — two routers' Radios — must not share,
// or steal, one socket.
func TestListenGivesEachCallerItsOwnSocket(t *testing.T) {
	a, pa, closeA, err := Listen("wash-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer closeA()
	b, pb, closeB, err := Listen("wash-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer closeB()
	if pa == pb {
		t.Fatalf("both listeners got %s", pa)
	}
	for _, c := range []struct {
		ln   net.Listener
		path string
	}{{a, pa}, {b, pb}} {
		go func(ln net.Listener) {
			if conn, err := ln.Accept(); err == nil {
				conn.Close()
			}
		}(c.ln)
		conn, err := net.Dial("unix", c.path)
		if err != nil {
			t.Fatalf("dial %s: %v", c.path, err)
		}
		conn.Close()
	}
	closeA()
	closeA()
	if _, err := os.Stat(filepath.Dir(pa)); !os.IsNotExist(err) {
		t.Fatalf("close left %s behind (err=%v)", filepath.Dir(pa), err)
	}
}
