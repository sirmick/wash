package router

import (
	"reflect"
	"testing"
)

// openArgs is the one place both spawn paths (open routing's spawnForOpen
// and spawn.request's optional Open) build the launch argv, so its shape
// is pinned here: nil for no path (spawnAndRun then appends nothing) and
// exactly ["--open", path] otherwise, which Conn.LaunchOpenPath parses.
func TestOpenArgs(t *testing.T) {
	if got := openArgs(""); got != nil {
		t.Fatalf("openArgs(\"\") = %v, want nil", got)
	}
	want := []string{"--open", "/home/u/docs/x.txt"}
	if got := openArgs("/home/u/docs/x.txt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("openArgs = %v, want %v", got, want)
	}
}
