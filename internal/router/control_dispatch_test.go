package router

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestControlDispatchRouting proves the two-phase decode in
// handleControl routes each v0.1 verb to its own handler after the
// controlReq god-struct was split into launchMsgReq / privRunReq.
//
// Each verb is driven with a request that fails fast inside its
// specific handler, and the distinctive error response is the proof
// that (a) the header peek picked the right verb and (b) the
// verb-specific re-decode populated the fields the handler reads:
//
//	launch   → "not_found" for an unregistered app_id (controlLaunch
//	           read AppID off the bytes)
//	msg      → "bad_request"/"missing instance_id" (controlMsg ran)
//	priv.run → "bad_request"/"priv.run requires req_id" (controlPrivRun
//	           ran and read ReqID="")
//	junk t   → "bad_request"/"unknown t" (default arm)
func TestControlDispatchRouting(t *testing.T) {
	tmpDir := t.TempDir()
	sock := filepath.Join(tmpDir, "ctl.sock")

	r := NewRouter(Config{
		ControlSocket: sock,
		AllowUID:      uint32(os.Getuid()),
		NoSession:     true,
		SessionAppID:  "com.wash.session",
	}, NewRegistry(), func(format string, args ...any) {
		t.Logf("router: "+format, args...)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenerErr := make(chan error, 1)
	go func() { listenerErr <- r.ListenControl(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("control socket never appeared: %v", err)
	}

	cases := []struct {
		name     string
		req      map[string]any
		wantCode string
	}{
		{
			name:     "launch routes to controlLaunch",
			req:      map[string]any{"t": "launch", "app_id": "com.wash.nonexistent"},
			wantCode: "not_found",
		},
		{
			name:     "msg routes to controlMsg",
			req:      map[string]any{"t": "msg"},
			wantCode: "bad_request", // missing instance_id
		},
		{
			name:     "priv.run routes to controlPrivRun",
			req:      map[string]any{"t": "priv.run"}, // missing req_id
			wantCode: "bad_request",
		},
		{
			name:     "unknown verb hits default",
			req:      map[string]any{"t": "nope"},
			wantCode: "bad_request",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := controlRoundTrip(t, sock, tc.req)
			if resp["t"] != "error" {
				t.Fatalf("got t=%v, want error: %v", resp["t"], resp)
			}
			if resp["code"] != tc.wantCode {
				t.Fatalf("got code=%v, want %q: %v", resp["code"], tc.wantCode, resp)
			}
		})
	}

	cancel()
	select {
	case err := <-listenerErr:
		if err != nil {
			t.Errorf("listener returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("listener did not shut down within 2s after ctx cancel")
	}
}

// controlRoundTrip dials the control socket, writes one JSON request
// line, and returns the decoded single-line JSON response.
func controlRoundTrip(t *testing.T, sock string, req map[string]any) map[string]any {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}
	defer conn.Close()

	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	b = append(b, '\n')
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write(b); err != nil {
		t.Fatalf("write req: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode resp %q: %v", line, err)
	}
	return resp
}
