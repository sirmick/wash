package router

import (
	"reflect"
	"testing"
)

// The --dev self-reload used to exec the RESOLVED binary under its own
// name. In the multicall layout that is `out/wash`, whose dispatcher
// reads argv[0] to pick a verb — so the reloaded router read `--listen`
// as a subcommand, printed usage and exited. The first rebuild of a
// `--dev` session killed the router instead of reloading it.
func TestReexecArgvKeepsTheInvokedName(t *testing.T) {
	got := reexecArgv([]string{"out/wash-router", "--listen", "0.0.0.0:11000", "--dev"})
	want := []string{"out/wash-router", "--listen", "0.0.0.0:11000", "--dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reexecArgv = %q, want %q", got, want)
	}
}

func TestReexecArgvDoesNotAliasItsInput(t *testing.T) {
	// append onto a slice header that still has spare capacity would
	// scribble on os.Args itself.
	args := make([]string, 2, 8)
	args[0], args[1] = "wash-router", "--dev"
	got := reexecArgv(args)
	got[1] = "--clobbered"
	if args[1] != "--dev" {
		t.Fatalf("reexecArgv aliased its input: args = %q", args)
	}
	if got[0] != "wash-router" {
		t.Fatalf("argv[0] = %q, want the invoked name", got[0])
	}
}

func TestReexecArgvEmpty(t *testing.T) {
	if got := reexecArgv(nil); got != nil {
		t.Fatalf("reexecArgv(nil) = %q, want nil", got)
	}
}
