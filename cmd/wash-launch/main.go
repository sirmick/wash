// wash-launch — tiny CLI that talks to a running wash-router via
// its control socket. Discovers the router via WASH_CONTROL_SOCKET
// (set by the router in every spawned app's env, and therefore
// inherited by shells running inside wash-term) or --socket.
//
// Subcommands:
//
//   wash-launch launch <app-id>
//   wash-launch <app-id>                       (back-compat shorthand)
//       Spawn an app. Prints "launched ... instance=i-N window=W".
//
//   wash-launch msg <instance-id> <json>
//       Relay a JSON payload as an APP_MSG to the named instance.
//       With --await-id, also wait for the BE's reply (matched by
//       the payload's "id" field) and print it as JSON.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"
)

const version = "0.0.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	sockFlag := flag.String("socket", "", "control socket path (overrides WASH_CONTROL_SOCKET)")
	timeoutSec := flag.Int("timeout", 5, "dial timeout in seconds")
	awaitID := flag.String("await-id", "", "(msg) wait for a BE reply whose data.id matches this value, and print it")
	awaitMs := flag.Int("await-ms", 0, "(msg) timeout in ms for --await-id (default 5000)")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("wash-launch %s\n", version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	sock := *sockFlag
	if sock == "" {
		sock = os.Getenv("WASH_CONTROL_SOCKET")
	}
	if sock == "" {
		fmt.Fprintln(os.Stderr, "wash-launch: no socket (set WASH_CONTROL_SOCKET or --socket)")
		os.Exit(1)
	}

	// Subcommand dispatch. The bare form `wash-launch <app-id>` is
	// kept working for backward compatibility — any first arg that
	// isn't a known verb is treated as an app id to launch.
	switch args[0] {
	case "launch":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: wash-launch launch <app-id>")
			os.Exit(2)
		}
		runLaunch(sock, *timeoutSec, args[1])
	case "msg":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: wash-launch msg <instance-id> <json>")
			os.Exit(2)
		}
		runMsg(sock, *timeoutSec, args[1], args[2], *awaitID, *awaitMs)
	default:
		if len(args) != 1 {
			usage()
			os.Exit(2)
		}
		runLaunch(sock, *timeoutSec, args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  wash-launch [flags] launch <app-id>")
	fmt.Fprintln(os.Stderr, "  wash-launch [flags] msg <instance-id> <json>")
	fmt.Fprintln(os.Stderr, "  wash-launch [flags] <app-id>   (shorthand for launch)")
	fmt.Fprintln(os.Stderr, "")
	flag.PrintDefaults()
}

func runLaunch(sock string, timeoutSec int, appID string) {
	req := map[string]any{"t": "launch", "app_id": appID}
	resp := roundtrip(sock, timeoutSec, req)
	if t, _ := resp["t"].(string); t == "error" {
		errorAndExit(resp)
	}
	fmt.Printf("launched %s (instance=%v window=%v)\n",
		appID, resp["instance_id"], resp["window_id"])
}

func runMsg(sock string, timeoutSec int, instanceID, payload, awaitID string, awaitMs int) {
	var data any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: msg payload is not JSON: %v\n", err)
		os.Exit(2)
	}
	req := map[string]any{"t": "msg", "instance_id": instanceID, "data": data}
	if awaitID != "" {
		req["await_id"] = awaitID
		if awaitMs > 0 {
			req["timeout_ms"] = awaitMs
		}
	}
	resp := roundtrip(sock, timeoutSec, req)
	if t, _ := resp["t"].(string); t == "error" {
		errorAndExit(resp)
	}
	// On `msg.reply`, print the reply payload as JSON to stdout so
	// it's pipe-friendly. On `msg.ok`, print nothing (matches a
	// silent unix tool — success is the absence of output).
	if t, _ := resp["t"].(string); t == "msg.reply" {
		out, err := json.Marshal(resp["data"])
		if err != nil {
			fmt.Fprintf(os.Stderr, "wash-launch: marshal reply: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
	}
}

// roundtrip dials the control socket, writes one JSON request, and
// reads the single-line JSON response. Exits with code 1 on any
// transport error (the JSON-level "t":"error" is handled by the
// caller so it can format the message).
func roundtrip(sock string, timeoutSec int, req map[string]any) map[string]any {
	conn, err := net.DialTimeout("unix", sock, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: dial %s: %v\n", sock, err)
		os.Exit(1)
	}
	defer conn.Close()

	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err := conn.Write(b); err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: write: %v\n", err)
		os.Exit(1)
	}
	rd := bufio.NewReader(conn)
	line, err := rd.ReadBytes('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: read: %v\n", err)
		os.Exit(1)
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: parse: %v\n", err)
		os.Exit(1)
	}
	return resp
}

func errorAndExit(resp map[string]any) {
	code, _ := resp["code"].(string)
	msg, _ := resp["msg"].(string)
	fmt.Fprintf(os.Stderr, "wash-launch: %s: %s\n", code, msg)
	os.Exit(1)
}
