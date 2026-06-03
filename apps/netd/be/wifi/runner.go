package wifi

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runner is the exec seam (mirrors the netplan backend): run returns combined
// output (for error text on mutations), runStdout returns STDOUT only — nmcli
// writes results to stdout and warnings/errors to stderr, and mixing stderr
// into terse output would corrupt the parser (the bug the netplan reader hit).
// Tests inject a fake that records calls and returns canned stdout.
type runner interface {
	run(ctx context.Context, name string, args ...string) (string, error)
	runStdout(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (execRunner) runStdout(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output() // stdout only
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return string(out), fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, stderr)
	}
	return string(out), nil
}
