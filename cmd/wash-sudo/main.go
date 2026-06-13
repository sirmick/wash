// wash-sudo — the CLI face of wash-priv.
//
// `wash-sudo cmd args…` asks wash-priv to run `cmd args…` as root.
// Approval and password entry happen in wash-priv's browser FE
// (https://localhost:<port>/) — never in this terminal. The
// invocation behaves like classic sudo otherwise: stdin / stdout /
// stderr stream through, exit code propagates.
//
// Crucially: the password never enters the PTY. The shell history,
// terminal scrollback, and screen-recording surfaces never see it.
// One unlock in the FE serves every wash-sudo invocation for the
// next 15 minutes (configurable).
//
// Flags:
//
//	--reason "why"            text shown in the FE approval row
//	--window                  spawn a wash-term window for the command
//	                          instead of inline streaming
//	--app com.wash.fm         spawn a registered wash app as root
//	                          (mutually exclusive with positional argv)
//	--preserve-env=VAR1,VAR2  comma-separated vars to forward
//	--no-prompt               fail immediately if the FE isn't unlocked
//	--lock                    just lock wash-priv's cache; no command
//	--socket PATH             control socket (else WASH_CONTROL_SOCKET)
//
// Exit code = wrapped command's exit, OR 1 on rejected/timeout/etc.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const version = "0.8.0"

func main() {
	reason := flag.String("reason", "", "freeform note shown in wash-priv's approval row")
	window := flag.Bool("window", false, "spawn a wash-term window instead of inline streaming")
	appID := flag.String("app", "", "spawn a registered wash app as root (no command argv)")
	preserveEnv := flag.String("preserve-env", "", "comma-separated env vars to forward to the command")
	noPrompt := flag.Bool("no-prompt", false, "fail immediately if wash-priv is locked")
	lock := flag.Bool("lock", false, "lock wash-priv's password cache (no command run)")
	sockFlag := flag.String("socket", "", "control socket path (overrides WASH_CONTROL_SOCKET)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("wash-sudo %s\n", version)
		return
	}

	sock := resolveSocket(*sockFlag)
	if sock == "" {
		fmt.Fprintln(os.Stderr, "wash-sudo: no control socket — set WASH_CONTROL_SOCKET or --socket, or run inside a wash terminal")
		os.Exit(1)
	}

	if *lock {
		os.Exit(runLock(sock))
	}

	// Decide what we're asking wash-priv to do.
	argv := flag.Args()
	switch {
	case *appID != "":
		if len(argv) != 0 {
			fmt.Fprintln(os.Stderr, "wash-sudo: --app and positional argv are mutually exclusive")
			os.Exit(2)
		}
		os.Exit(runApp(sock, *appID, *reason, *noPrompt))
	case len(argv) == 0:
		usage()
		os.Exit(2)
	default:
		os.Exit(runCommand(sock, argv, *reason, *window, *noPrompt, parseEnvList(*preserveEnv)))
	}
}

// resolveSocket picks the control socket path. Priority:
//   1. --socket flag
//   2. WASH_CONTROL_SOCKET env
//   3. Conventional default /tmp/wash-<uid>.sock (matches router's default)
func resolveSocket(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("WASH_CONTROL_SOCKET"); v != "" {
		return v
	}
	// Fallback default — the router writes this when --control-socket
	// is empty. Lets wash-sudo work from a shell that didn't
	// inherit the env (e.g. nested ssh, fresh tmux pane).
	def := fmt.Sprintf("/tmp/wash-%d.sock", os.Getuid())
	if _, err := os.Stat(def); err == nil {
		return def
	}
	return ""
}

// parseEnvList turns "VAR1,VAR2" into {"VAR1": "current value",
// "VAR2": "current value"}. Vars not in the current env are
// silently dropped — passing an unset var would just leak the
// emptiness without value, and sudo's --preserve-env errors on
// unknown names anyway.
func parseEnvList(s string) map[string]string {
	if s == "" {
		return nil
	}
	out := map[string]string{}
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if v, ok := os.LookupEnv(name); ok {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dial(sock string) (net.Conn, error) {
	return net.Dial("unix", sock)
}

// newReqID returns a short random hex id suitable for the priv.run
// req_id field. Doesn't need to be cryptographically secure — just
// has to be unique within wash-priv's queue at any moment.
func newReqID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "ws-" + hex.EncodeToString(b[:])
}

// runCommand is the main path: inline (or --window) execution of an
// argv. Sets up the streaming control conn, pumps stdin one way
// and stdout/stderr the other, exits with the wrapped command's
// code.
func runCommand(sock string, argv []string, reason string, window bool, noPrompt bool, env map[string]string) int {
	// reqID exists before the dial so even a dial failure is
	// correlatable with wash-priv's queue / audit log.
	reqID := newReqID()
	conn, err := dial(sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-sudo: req_id=%s dial %s: %v\n", reqID, sock, err)
		return 1
	}
	defer conn.Close()

	cwd, _ := os.Getwd()
	req := map[string]any{
		"t":      "priv.run",
		"req_id": reqID,
		"argv":   argv,
		"cwd":    cwd,
	}
	if reason != "" {
		req["reason"] = reason
	}
	if window {
		req["window"] = true
	}
	if noPrompt {
		req["no_prompt"] = true
	}
	if env != nil {
		req["env"] = env
	}
	if err := writeJSON(conn, req); err != nil {
		fmt.Fprintf(os.Stderr, "wash-sudo: write request: %v\n", err)
		return 1
	}
	return drive(conn, req["req_id"].(string), !window)
}

// runApp is --app: spawn a registered wash app as root via the
// existing wash-priv spawn path. We exit as soon as the spawn fires
// — the launched app is long-running and has its own window; there's
// nothing for wash-sudo to wait on once PrepareSpawn returns.
func runApp(sock string, appID, reason string, noPrompt bool) int {
	conn, err := dial(sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-sudo: dial %s: %v\n", sock, err)
		return 1
	}
	defer conn.Close()
	req := map[string]any{
		"t":         "priv.run",
		"req_id":    newReqID(),
		"app_id":    appID,
		"no_prompt": noPrompt,
		"reason":    reason,
	}
	if err := writeJSON(conn, req); err != nil {
		fmt.Fprintf(os.Stderr, "wash-sudo: write request: %v\n", err)
		return 1
	}
	// inline=false: wash-sudo returns on the first "spawned" event
	// rather than waiting for a priv.result (the spawned app stays
	// alive in its own window).
	return drive(conn, req["req_id"].(string), false)
}

// runLock asks wash-priv to clear its password cache. Implemented
// as a normal control-socket `msg` to the singleton's well-known
// instance id... but we don't have the instance id here. For now,
// document the gap and exit cleanly; the FE Lock button does the
// job.
func runLock(sock string) int {
	fmt.Fprintln(os.Stderr, "wash-sudo: --lock not yet implemented — use the Lock button in wash-priv's window")
	return 1
}

// drive is the conn-driving loop: writes our stdin to the conn,
// reads JSON envelopes from the conn, fans stdout/stderr to local
// streams, returns the exit code on priv.result.
//
// Lifecycle: when priv.result arrives we return immediately. The
// stdin-forwarding goroutine (if any) is left blocked on os.Stdin;
// the kernel reaps it when our process exits. We DON'T wait for
// stdin EOF — that never happens on an interactive tty and the
// process would hang after the command itself was already done.
func drive(conn net.Conn, reqID string, inline bool) int {
	// Forwarder: our stdin → priv.stdin messages. Only run inline
	// commands need this; --window targets receive their own pty
	// stdin via the wash-term window.
	if inline {
		go forwardStdin(conn, reqID)
	}

	rd := bufio.NewReader(conn)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return 1
			}
			fmt.Fprintf(os.Stderr, "wash-sudo: read: %v\n", err)
			return 1
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev["t"] {
		case "priv.registered":
			// ack only; nothing to do
		case "priv.event":
			// the inner payload from wash-priv
			data, _ := ev["data"].(map[string]any)
			if data == nil {
				continue
			}
			switch data["kind"] {
			case "priv.stream":
				stream, _ := data["stream"].(string)
				b := decodeB64(data["bytes"])
				if stream == "stderr" {
					_, _ = os.Stderr.Write(b)
				} else {
					_, _ = os.Stdout.Write(b)
				}
			case "priv.result", "result":
				exit := toInt(data["exit_code"])
				if errMsg, _ := data["error"].(string); errMsg != "" {
					fmt.Fprintf(os.Stderr, "wash-sudo: %s\n", errMsg)
				}
				return exit
			case "rejected":
				reason, _ := data["reason"].(string)
				if reason == "" {
					reason = "rejected by user"
				}
				fmt.Fprintf(os.Stderr, "wash-sudo: %s\n", reason)
				return 1
			case "spawned":
				// Spawn-style requests (explicit --app, or bare argv
				// that the router heuristically promoted to spawn)
				// finish here — the launched app owns its lifecycle
				// in its own window. inline=true requests that get a
				// spawned event are the auto-promotion case; same
				// success semantics.
				return 0
			case "error":
				code, _ := data["code"].(string)
				msg, _ := data["msg"].(string)
				fmt.Fprintf(os.Stderr, "wash-sudo: %s: %s\n", code, msg)
				return 1
			}
		case "priv.error":
			code, _ := ev["code"].(string)
			msg, _ := ev["msg"].(string)
			fmt.Fprintf(os.Stderr, "wash-sudo: %s: %s\n", code, msg)
			return 1
		case "error":
			code, _ := ev["code"].(string)
			msg, _ := ev["msg"].(string)
			fmt.Fprintf(os.Stderr, "wash-sudo: %s: %s\n", code, msg)
			return 1
		}
	}
}

// forwardStdin shovels our stdin into the conn as priv.stdin
// envelopes. Runs until stdin EOFs or our process exits.
func forwardStdin(conn net.Conn, reqID string) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			payload := map[string]any{
				"t":      "priv.stdin",
				"req_id": reqID,
				"bytes":  base64.StdEncoding.EncodeToString(buf[:n]),
			}
			_ = writeJSON(conn, payload)
		}
		if err != nil {
			_ = writeJSON(conn, map[string]any{"t": "priv.stdin.close", "req_id": reqID})
			return
		}
	}
}

func writeJSON(conn net.Conn, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = conn.Write(b)
	return err
}

func decodeB64(v any) []byte {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

func usage() {
	prog := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `usage: %s [flags] cmd [args...]
       %s --app <app-id> [flags]
       %s --lock

flags:
  -reason "why"             freeform note in wash-priv's approval row
  -window                   spawn a wash-term window instead of inline
  -preserve-env VAR1,VAR2   forward env vars to the command
  -no-prompt                fail immediately if wash-priv is locked
  -socket PATH              control socket path
`, prog, prog, prog)
}
