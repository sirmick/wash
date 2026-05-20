package router

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// probeTimeout is the cap on a --wash-manifest exec (WIRE.md §5).
const probeTimeout = 2 * time.Second

// probeStdoutCap is the max bytes read from probe stdout (WIRE.md §5).
const probeStdoutCap = 64 * 1024

// Probe runs `<bin> --wash-manifest` with a stripped environment and
// returns the captured stdout (WIRE.md §5). Stdout is capped at 64
// KiB; the process is killed if it exceeds probeTimeout.
//
// The function returns the bytes captured even if the process exits
// with a non-zero status — the registry may want to surface the
// truncated output in the disable reason.
func Probe(ctx context.Context, binary string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--wash-manifest")
	cmd.Env = []string{"WASH_PROTO=1"}

	lw := &limitedWriter{cap: probeStdoutCap}
	cmd.Stdout = lw
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return lw.bytes(), fmt.Errorf("probe %s: timed out after %s", binary, probeTimeout)
		}
		return lw.bytes(), fmt.Errorf("probe %s: %w", binary, err)
	}
	return lw.bytes(), nil
}

// limitedWriter accumulates up to cap bytes, discarding the rest. It
// never returns a short-write error so the probed process is not
// killed early on EPIPE — we want it to exit on its own.
type limitedWriter struct {
	cap int
	buf []byte
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(l.buf) >= l.cap {
		return len(p), nil
	}
	remaining := l.cap - len(l.buf)
	if remaining > len(p) {
		l.buf = append(l.buf, p...)
		return len(p), nil
	}
	l.buf = append(l.buf, p[:remaining]...)
	return len(p), nil
}

func (l *limitedWriter) bytes() []byte { return l.buf }
