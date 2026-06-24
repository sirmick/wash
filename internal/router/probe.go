package router

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// probeTimeout is the cap on a --wash-manifest exec (WIRE.md §5).
const probeTimeout = 2 * time.Second

// probeStdoutCap is the max bytes read from probe stdout (WIRE.md §5).
// Raised to 32 MiB so the embedded FE bundle(s) can ride along in the
// framed probe payload (header line + raw main/panel bundles). The
// router's max frame size is 16 MiB; bundles now travel raw (no base64
// expansion), so two ~12 MiB bundles still fit. No realistic wash app
// is anywhere near that — the largest in tree is ~2 MiB.
const probeStdoutCap = 32 * 1024 * 1024

// probeStderrCap bounds the stderr we keep for diagnostics. A misbehaving
// binary's stderr is the single most useful clue when its stdout has no
// manifest, so we capture a snippet (not io.Discard) and weave it into the
// error / disable reason.
const probeStderrCap = 4 * 1024

// Probe runs `<bin> --wash-manifest` with a stripped environment and
// returns the captured stdout (WIRE.md §5). Stdout is capped at 64
// KiB; the process is killed if it exceeds probeTimeout.
//
// The function returns the bytes captured even if the process exits
// with a non-zero status — the registry may want to surface the
// truncated output in the disable reason. Any stderr the binary emitted
// is folded into the returned error so an operator can see why a probe
// produced no usable manifest.
func Probe(ctx context.Context, binary string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--wash-manifest")
	cmd.Env = []string{"WASH_PROTO=1"}

	lw := &limitedWriter{cap: probeStdoutCap}
	ew := &limitedWriter{cap: probeStderrCap}
	cmd.Stdout = lw
	cmd.Stderr = ew

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return lw.bytes(), fmt.Errorf("probe %s: timed out after %s%s", binary, probeTimeout, stderrSuffix(ew.bytes()))
		}
		return lw.bytes(), fmt.Errorf("probe %s: %w%s", binary, err, stderrSuffix(ew.bytes()))
	}
	return lw.bytes(), nil
}

// stderrSuffix renders captured probe stderr as a " (stderr: …)" clause, or
// "" when the binary was silent. Trailing whitespace is trimmed so the
// reason stays one line.
func stderrSuffix(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	return " (stderr: " + s + ")"
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
