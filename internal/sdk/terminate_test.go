package sdk

import (
	"reflect"
	"testing"
)

// resetTerminateState clears the package-level hook registry so a test
// starts from a known state. It does NOT reset termInstalled — the signal
// handler goroutine, once started, is harmless and we never send it a
// signal in-process (that would os.Exit the test binary).
func resetTerminateState() {
	termMu.Lock()
	termHooks = nil
	termMu.Unlock()
}

func TestOnTerminateRunsHooksLIFO(t *testing.T) {
	resetTerminateState()
	t.Cleanup(resetTerminateState)

	var order []int
	OnTerminate(func() { order = append(order, 1) })
	OnTerminate(func() { order = append(order, 2) })
	OnTerminate(func() { order = append(order, 3) })

	runTerminateHooks()

	// Newest-first: a service that registered late (e.g. after spawning a
	// child) gets to tear down before earlier hooks.
	if want := []int{3, 2, 1}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
}

func TestOnTerminateNilHookIsNoOp(t *testing.T) {
	resetTerminateState()
	t.Cleanup(resetTerminateState)

	// A nil hook (the coverage-flush ensure-installed case) registers no
	// cleanup but must not panic when the hooks run.
	OnTerminate(nil)
	runTerminateHooks() // must not panic with an empty/ nil-only registry
}

func TestRunTerminateHooksContainsPanic(t *testing.T) {
	resetTerminateState()
	t.Cleanup(resetTerminateState)

	var ran []string
	OnTerminate(func() { ran = append(ran, "a") })
	OnTerminate(func() { panic("boom") }) // a panicky hook must not stop the rest
	OnTerminate(func() { ran = append(ran, "c") })

	runTerminateHooks()

	// c (newest non-panicking) and a (oldest) both ran despite the middle
	// hook panicking.
	if want := []string{"c", "a"}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran = %v, want %v (panic in one hook must not block the others)", ran, want)
	}
}
