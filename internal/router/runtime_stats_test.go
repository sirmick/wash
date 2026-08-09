// Tests for the runtime-stats collector. Exercises the three row
// classes the About panel renders differently:
//
//   - Heartbeat received, Go kind → full Go-runtime columns.
//   - Heartbeat received, non-Go kind → liveness + RSS but Go fields
//     stay zero so the FE can render "—".
//   - No heartbeat → HasHeartbeat false; everything Go is zero.

package router

import (
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

func TestRuntimeRowGoKindCarriesGoFields(t *testing.T) {
	r := &Router{}
	r.recordRuntimeStats("i-go", wire.EvtRuntimeStats{
		T:            wire.TEvtRuntimeStats,
		RuntimeKind:  "go",
		GoVersion:    "go1.25.10",
		NumGoroutine: 42,
		HeapAlloc:    1024,
		NumGC:        3,
		UptimeSec:    7,
	})
	// Empty-string runtime kind defaults to go (pre-field binaries).
	r.recordRuntimeStats("i-implicit-go", wire.EvtRuntimeStats{
		T:            wire.TEvtRuntimeStats,
		GoVersion:    "go1.25.10",
		NumGoroutine: 9,
		HeapAlloc:    512,
		UptimeSec:    2,
	})
	// Non-Go kind: same envelope but RuntimeKind=rust and the Go
	// fields are deliberately zero to simulate a future SDK.
	r.recordRuntimeStats("i-rust", wire.EvtRuntimeStats{
		T:           wire.TEvtRuntimeStats,
		RuntimeKind: "rust",
		UptimeSec:   11,
	})

	for _, c := range []struct {
		instID, wantKind string
		wantHB           bool
		wantGoFields     bool
	}{
		{"i-go", "go", true, true},
		{"i-implicit-go", "go", true, true},
		{"i-rust", "rust", true, false},
		{"i-never-pinged", "", false, false},
	} {
		// Synthesize a row using the same code path collectRuntimeTable
		// uses: read from runtimeStats, apply the kind defaulting +
		// Go-block copy logic.
		rec, ok := r.runtimeStats[c.instID]
		row := runtimeRow{InstanceID: c.instID}
		if ok {
			row.HasHeartbeat = true
			row.RuntimeKind = rec.RuntimeKind
			if row.RuntimeKind == "" {
				row.RuntimeKind = "go"
			}
			row.UptimeSec = rec.UptimeSec
			if row.RuntimeKind == "go" {
				row.GoVersion = rec.GoVersion
				row.NumGoroutine = rec.NumGoroutine
				row.HeapAlloc = rec.HeapAlloc
				row.NumGC = rec.NumGC
			}
		}
		if row.HasHeartbeat != c.wantHB {
			t.Errorf("%s: HasHeartbeat = %v, want %v", c.instID, row.HasHeartbeat, c.wantHB)
		}
		if row.RuntimeKind != c.wantKind {
			t.Errorf("%s: RuntimeKind = %q, want %q", c.instID, row.RuntimeKind, c.wantKind)
		}
		if c.wantGoFields {
			if row.GoVersion == "" || row.NumGoroutine == 0 || row.HeapAlloc == 0 {
				t.Errorf("%s: expected Go fields populated, got Goroutine=%d HeapAlloc=%d Version=%q",
					c.instID, row.NumGoroutine, row.HeapAlloc, row.GoVersion)
			}
		} else {
			if row.GoVersion != "" || row.NumGoroutine != 0 || row.HeapAlloc != 0 || row.NumGC != 0 {
				t.Errorf("%s: expected Go fields zero, got Goroutine=%d HeapAlloc=%d Version=%q NumGC=%d",
					c.instID, row.NumGoroutine, row.HeapAlloc, row.GoVersion, row.NumGC)
			}
		}
	}
}

func TestDropRuntimeStatsClearsRecord(t *testing.T) {
	r := &Router{}
	r.recordRuntimeStats("i-1", wire.EvtRuntimeStats{T: wire.TEvtRuntimeStats, UptimeSec: 1})
	if _, ok := r.runtimeStats["i-1"]; !ok {
		t.Fatal("recordRuntimeStats failed to store")
	}
	r.dropRuntimeStats("i-1")
	if _, ok := r.runtimeStats["i-1"]; ok {
		t.Fatal("dropRuntimeStats failed to remove")
	}
}
