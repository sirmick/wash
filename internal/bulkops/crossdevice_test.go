package bulkops

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// isCrossDevice is what makes a queued move degrade to copy+delete; fm
// routes a single-item drag that rename(2) refused with EXDEV to the queue
// on that promise. It has to see through os.Rename's *LinkError.
func TestIsCrossDeviceSeesThroughLinkError(t *testing.T) {
	if !isCrossDevice(&os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: syscall.EXDEV}) {
		t.Fatal("EXDEV inside a LinkError must count as cross-device")
	}
	if isCrossDevice(&os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: syscall.EACCES}) {
		t.Fatal("EACCES must not count as cross-device")
	}
	if isCrossDevice(errors.New("plain")) || isCrossDevice(nil) {
		t.Fatal("non-EXDEV errors must not count as cross-device")
	}
}
