// wash-priv-fakesudo — sudo stub for wash-priv's e2e tests.
//
// Mimics just enough of sudo's `-S -k …` interface for the e2e
// harness to exercise wash-priv's password flow without real root.
// Pointed at via WASH_PRIV_SUDO_BIN in the test fixture.
//
// Supported invocations:
//
//	fakesudo -S -k -v
//	    Read pw\n from stdin. If pw == FAKESUDO_PASSWORD (default
//	    "test123") exit 0; otherwise exit 1. This is the "validate"
//	    path wash-priv uses before caching.
//
//	fakesudo -S -k --preserve-env=… -- <target> <args...>
//	    Read pw\n from stdin, check it, then exec the target with
//	    args. The user uid is unchanged (we're not setuid) — the
//	    point is that wash-priv's argv and env wiring are exercised
//	    end to end, not that the target actually runs as root.
//
// FAKESUDO_LOG (optional): path to append one JSON line per
// invocation. Tests use this to assert wash-priv actually called
// us with the expected argv + env.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// expectedPassword is the secret fakesudo accepts. Overridable via
// FAKESUDO_PASSWORD so tests can assert "wrong password" behavior
// with a separately-configured password.
func expectedPassword() string {
	if v := os.Getenv("FAKESUDO_PASSWORD"); v != "" {
		return v
	}
	return "test123"
}

type invocation struct {
	TS      string   `json:"ts"`
	Args    []string `json:"args"`
	Env     []string `json:"env,omitempty"` // only WASH_* and FAKESUDO_*
	Mode    string   `json:"mode"`          // "validate" | "exec" | "error"
	PWOK    bool     `json:"pw_ok"`
	Target  string   `json:"target,omitempty"`
	TargArg []string `json:"target_args,omitempty"`
	Exit    int      `json:"exit"`
	Err     string   `json:"err,omitempty"`
}

func main() {
	inv := invocation{
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Args: append([]string(nil), os.Args[1:]...),
		Env:  filterEnv(os.Environ()),
	}
	exit := run(&inv)
	inv.Exit = exit
	// For exec calls we already logged eagerly (with Exit=-1) so
	// tests don't have to wait for the wash app to terminate.
	// Skip the duplicate; tests reading the log see one entry per
	// invocation, with Exit=-1 meaning "dispatched but not yet
	// finished" — which is exactly what we want for the long-lived
	// wash apps wash-priv launches.
	if inv.Mode != "exec" {
		appendLog(inv)
	}
	os.Exit(exit)
}

// run implements the actual fakesudo logic. Returns the exit code;
// the caller is responsible for logging and process exit.
func run(inv *invocation) int {
	args := inv.Args
	if len(args) == 0 {
		inv.Mode = "error"
		inv.Err = "no args"
		return 2
	}
	// Walk past flags. We accept: -S -k --preserve-env=... -v
	// and then either nothing (validate-only with -v earlier) or
	// "--" target args...
	validateOnly := false
	rest := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-S", a == "-k":
			continue
		case a == "-v":
			validateOnly = true
		case strings.HasPrefix(a, "--preserve-env="):
			continue
		case a == "--":
			rest = args[i+1:]
			i = len(args)
		default:
			// Unknown flag — be permissive (sudo has many), treat as
			// validate to be safe. The e2e calls only the two shapes
			// documented above so this branch shouldn't fire.
		}
	}
	pwBytes, err := readPassword(os.Stdin)
	if err != nil {
		inv.Mode = "error"
		inv.Err = "read password: " + err.Error()
		return 2
	}
	pw := strings.TrimRight(string(pwBytes), "\n")
	inv.PWOK = (pw == expectedPassword())
	if validateOnly {
		inv.Mode = "validate"
		if !inv.PWOK {
			return 1
		}
		return 0
	}
	if len(rest) == 0 {
		inv.Mode = "error"
		inv.Err = "no target after --"
		return 2
	}
	if !inv.PWOK {
		inv.Mode = "exec"
		inv.Err = "bad password"
		return 1
	}
	inv.Mode = "exec"
	inv.Target = rest[0]
	if len(rest) > 1 {
		inv.TargArg = rest[1:]
	}
	// Log eagerly (with Exit=-1 sentinel) BEFORE c.Run so tests can
	// observe the dispatch without waiting for the spawned app to
	// exit. Most wash apps never auto-exit; the harness only cares
	// that the right argv crossed the sudo boundary.
	inv.Exit = -1
	appendLog(*inv)
	c := exec.Command(rest[0], rest[1:]...)
	// Inherit env — sudo's --preserve-env would have done this for
	// the named vars; fakesudo passes everything through so the
	// child can dial the router via WASH_DISPLAY etc.
	c.Env = os.Environ()
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = nil
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		inv.Err = err.Error()
		return 2
	}
	return 0
}

// readPassword consumes stdin up to the first newline. We don't bound
// the read because the BE side only writes pw\n; if more arrives,
// we'll see it on the next iteration of the loop (which never runs).
func readPassword(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	return []byte(line), nil
}

// filterEnv keeps only WASH_* and FAKESUDO_* entries so the log file
// stays auditable without dumping the user's whole environment.
func filterEnv(env []string) []string {
	out := []string{}
	for _, e := range env {
		if strings.HasPrefix(e, "WASH_") || strings.HasPrefix(e, "FAKESUDO_") {
			out = append(out, e)
		}
	}
	return out
}

// appendLog writes one JSON line to FAKESUDO_LOG if set. Best-effort.
func appendLog(inv invocation) {
	path := os.Getenv("FAKESUDO_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakesudo: open log %s: %v\n", path, err)
		return
	}
	defer f.Close()
	b, err := json.Marshal(inv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakesudo: marshal: %v\n", err)
		return
	}
	_, _ = f.Write(append(b, '\n'))
}
