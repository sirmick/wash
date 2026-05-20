// wash-launch — tiny CLI to ask the running wash-router to spawn an
// app. Discovers the router via WASH_CONTROL_SOCKET (set by the
// router in every spawned app's env, and therefore inherited by
// shells running inside wash-term) or --socket.
//
//   wash-launch com.wash.about
//   wash-launch --socket=/tmp/wash-1000.sock com.wash.test
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
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: wash-launch [--socket=path] [--timeout=N] <app-id>")
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("wash-launch %s\n", version)
		return
	}
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	appID := flag.Arg(0)

	sock := *sockFlag
	if sock == "" {
		sock = os.Getenv("WASH_CONTROL_SOCKET")
	}
	if sock == "" {
		fmt.Fprintln(os.Stderr, "wash-launch: no socket (set WASH_CONTROL_SOCKET or --socket)")
		os.Exit(1)
	}

	conn, err := net.DialTimeout("unix", sock, time.Duration(*timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wash-launch: dial %s: %v\n", sock, err)
		os.Exit(1)
	}
	defer conn.Close()

	req := map[string]any{"t": "launch", "app_id": appID}
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
	if t, _ := resp["t"].(string); t == "error" {
		code, _ := resp["code"].(string)
		msg, _ := resp["msg"].(string)
		fmt.Fprintf(os.Stderr, "wash-launch: %s: %s\n", code, msg)
		os.Exit(1)
	}
	fmt.Printf("launched %s (instance=%v window=%v)\n",
		appID, resp["instance_id"], resp["window_id"])
}
