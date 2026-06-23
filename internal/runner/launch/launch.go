// Package launch is the wash-launch CLI's compiled-in form.
//
// Both the standalone shim (cmd/wash-launch/main.go) and the
// multi-call dispatcher (cmd/wash, argv[0]="wash-launch" or
// `wash launch …` subcommand) call Run with the remaining argv
// slice. Run owns its own flag set so the multi-call host's
// argv[0]-dispatch doesn't have to interleave flag state.
//
// CLI shape:
//
//	wash-launch launch <app-id>
//	wash-launch <app-id>                       (back-compat shorthand)
//	    Spawn an app. Prints "launched ... instance=i-N window=W".
//
//	wash-launch msg <instance-id> <json>
//	    Relay a JSON payload as an APP_MSG to the named instance.
//	    With --await-id, also wait for the BE's reply (matched by
//	    the payload's "id" field) and print it as JSON.
package launch

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/sirmick/wash/internal/version"
	"io"
	"net"
	"os"
	"time"
)

// Run drives the wash-launch CLI with the given argv (excluding the
// program name). Returns a process exit code — 0 on success, non-zero
// on dial / parse / protocol failure. Callers use os.Exit on the
// returned value.
func Run(args []string) int {
	fs := flag.NewFlagSet("wash-launch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	sockFlag := fs.String("socket", "", "control socket path (overrides WASH_CONTROL_SOCKET)")
	timeoutSec := fs.Int("timeout", 5, "dial timeout in seconds")
	awaitID := fs.String("await-id", "", "(msg) wait for a BE reply whose data.id matches this value, and print it")
	awaitMs := fs.Int("await-ms", 0, "(msg) timeout in ms for --await-id (default 5000)")
	fs.Usage = func() { writeUsage(fs.Output(), fs) }
	if err := fs.Parse(args); err != nil {
		// flag already printed the message.
		return 2
	}

	if *showVersion {
		fmt.Printf("wash-launch %s\n", version.Version)
		return 0
	}

	pos := fs.Args()
	if len(pos) == 0 {
		writeUsage(os.Stderr, fs)
		return 2
	}

	sock := *sockFlag
	if sock == "" {
		sock = os.Getenv("WASH_CONTROL_SOCKET")
	}
	if sock == "" {
		fmt.Fprintln(os.Stderr, "wash-launch: no socket (set WASH_CONTROL_SOCKET or --socket)")
		return 1
	}

	switch pos[0] {
	case "launch":
		if len(pos) != 2 {
			fmt.Fprintln(os.Stderr, "usage: wash-launch launch <app-id>")
			return 2
		}
		return runLaunch(sock, *timeoutSec, pos[1])
	case "msg":
		if len(pos) != 3 {
			fmt.Fprintln(os.Stderr, "usage: wash-launch msg <instance-id> <json>")
			return 2
		}
		return runMsg(sock, *timeoutSec, pos[1], pos[2], *awaitID, *awaitMs)
	default:
		if len(pos) != 1 {
			writeUsage(os.Stderr, fs)
			return 2
		}
		return runLaunch(sock, *timeoutSec, pos[0])
	}
}

func writeUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  wash-launch [flags] launch <app-id>")
	fmt.Fprintln(w, "  wash-launch [flags] msg <instance-id> <json>")
	fmt.Fprintln(w, "  wash-launch [flags] <app-id>   (shorthand for launch)")
	fmt.Fprintln(w, "")
	fs.PrintDefaults()
}

func runLaunch(sock string, timeoutSec int, appID string) int {
	req := map[string]any{"t": "launch", "app_id": appID}
	resp, code := roundtrip(sock, timeoutSec, req)
	if code != 0 {
		return code
	}
	if t, _ := resp["t"].(string); t == "error" {
		return printErrAndExit(resp)
	}
	fmt.Printf("launched %s (instance=%v window=%v)\n",
		appID, resp["instance_id"], resp["window_id"])
	return 0
}

func runMsg(sock string, timeoutSec int, instanceID, payload, awaitID string, awaitMs int) int {
	var data any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: msg payload is not JSON: %v\n", err)
		return 2
	}
	req := map[string]any{"t": "msg", "instance_id": instanceID, "data": data}
	if awaitID != "" {
		req["await_id"] = awaitID
		if awaitMs > 0 {
			req["timeout_ms"] = awaitMs
		}
	}
	resp, code := roundtrip(sock, timeoutSec, req)
	if code != 0 {
		return code
	}
	if t, _ := resp["t"].(string); t == "error" {
		return printErrAndExit(resp)
	}
	// On `msg.reply`, print the reply payload as JSON to stdout so
	// it's pipe-friendly. On `msg.ok`, print nothing (matches a
	// silent unix tool — success is the absence of output).
	if t, _ := resp["t"].(string); t == "msg.reply" {
		out, err := json.Marshal(resp["data"])
		if err != nil {
			fmt.Fprintf(os.Stderr, "wash-launch: marshal reply: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
	}
	return 0
}

// roundtrip dials the control socket, writes one JSON request, and
// reads the single-line JSON response. Returns (response, 0) on
// success or (nil, exitCode) on any transport error. JSON-level
// {"t":"error"} responses come back as a normal response — the
// caller inspects t and routes through printErrAndExit.
func roundtrip(sock string, timeoutSec int, req map[string]any) (map[string]any, int) {
	conn, err := net.DialTimeout("unix", sock, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: dial %s: %v\n", sock, err)
		return nil, 1
	}
	defer conn.Close()

	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: write: %v\n", err)
		return nil, 1
	}
	rd := bufio.NewReader(conn)
	line, err := rd.ReadBytes('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: read: %v\n", err)
		return nil, 1
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: parse: %v\n", err)
		return nil, 1
	}
	return resp, 0
}

func printErrAndExit(resp map[string]any) int {
	code, _ := resp["code"].(string)
	msg, _ := resp["msg"].(string)
	fmt.Fprintf(os.Stderr, "wash-launch: %s: %s\n", code, msg)
	return 1
}
