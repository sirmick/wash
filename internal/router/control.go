// Control socket: a local Unix endpoint the router opens at
// cfg.ControlSocket so tools like wash-launch can ask for app
// spawns without speaking the full wash protocol or being spawned
// by the router themselves. Single-user trust model — the socket
// is chmod 0600; "anyone with the path is the same user."
//
// Protocol is intentionally tiny: line-delimited JSON, one request
// per connection. v0.1 supports a single message type.
//
//   request:  {"t":"launch","app_id":"com.wash.about"}
//   response: {"t":"launched","instance_id":"i-5","window_id":3}
//          or {"t":"error","code":"not_found","msg":"..."}
//
// We do not extend the wash wire protocol for this — it's a
// separate, simpler transport. Keeps the architecture's "trust by
// inheritance" boundary intact for app↔router and adds a clearly
// scoped local-process side channel for tooling.

package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// ListenControl opens the control socket and serves it until ctx
// cancels. A no-op (returns nil) when cfg.ControlSocket is empty.
func (r *Router) ListenControl(ctx context.Context) error {
	path := r.cfg.ControlSocket
	if path == "" {
		return nil
	}
	// Best-effort: remove any stale socket from a previous run.
	_ = os.Remove(path)
	lis, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen control %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		_ = os.Remove(path)
		return fmt.Errorf("chmod control: %w", err)
	}

	r.log("control socket listening on %s", path)
	go func() {
		<-ctx.Done()
		_ = lis.Close()
		_ = os.Remove(path)
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			r.log("control accept: %v", err)
			continue
		}
		go r.handleControl(ctx, conn)
	}
}

func (r *Router) handleControl(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	line, err := rd.ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		T     string `json:"t"`
		AppID string `json:"app_id"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": err.Error(),
		})
		return
	}
	switch req.T {
	case "launch":
		r.controlLaunch(ctx, conn, req.AppID)
	default:
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": "unknown t",
		})
	}
}

func (r *Router) controlLaunch(ctx context.Context, conn net.Conn, appID string) {
	if appID == "" {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": "missing app_id",
		})
		return
	}
	entry := r.reg.ByID(appID)
	if entry == nil || !entry.Enabled() {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "not_found", "msg": appID,
		})
		return
	}
	// Spawning a session-surface app via the control socket would
	// stomp on the autoboot session; refuse.
	if entry.Manifest.Surface == SurfaceDesktop {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "forbidden", "msg": "cannot launch desktop-surface app via control socket",
		})
		return
	}
	inst, err := r.spawnAndRun(ctx, entry, false)
	if err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "internal", "msg": err.Error(),
		})
		return
	}
	writeControlResponse(conn, map[string]any{
		"t":           "launched",
		"instance_id": inst.InstanceID,
		"window_id":   uint64(inst.WindowID),
	})
}

func writeControlResponse(conn net.Conn, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = conn.Write(b)
}
