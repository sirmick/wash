// Control socket: a local Unix endpoint the router opens at
// cfg.ControlSocket so tools like wash-launch can ask for app
// spawns without speaking the full wash protocol or being spawned
// by the router themselves. Single-user trust model — the socket
// is chmod 0600; "anyone with the path is the same user."
//
// Protocol is intentionally tiny: line-delimited JSON, one request
// per connection. v0.1 supports two message types:
//
//   request:  {"t":"launch","app_id":"com.wash.about"}
//   response: {"t":"launched","instance_id":"i-5","window_id":3}
//   error:    {"t":"error","code":"not_found","msg":"..."}
//
//   request:  {"t":"msg","instance_id":"i-5","data":{...}}
//              (optionally) "await_id":"r1","timeout_ms":3000
//   response: {"t":"msg.ok"}                    when fire-and-forget
//   response: {"t":"msg.reply","data":{...}}    when await_id matched
//   error:    {"t":"error","code":"timeout"...}
//
// `msg` relays the data verbatim as an APP_MSG event to the named
// instance — semantically identical to a shell `app_msg.send`, but
// reachable from a CLI / test harness. When the request includes
// `await_id`, the router installs a one-shot watcher matching that
// id against the BE's outbound app_msg `data.id` field; the matched
// reply is returned over the same socket.
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
	"time"

	"github.com/sirmick/wash/internal/wire"
)

// defaultMsgAwaitTimeout is the wait cap when a `msg` request set
// await_id without an explicit timeout_ms. Keep it tight so a
// non-responsive BE doesn't block the caller forever.
const defaultMsgAwaitTimeout = 5 * time.Second

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

// controlReq is the union of fields any v0.1 control message may
// carry. Unused fields stay zero for a given op.
type controlReq struct {
	T          string          `json:"t"`
	AppID      string          `json:"app_id,omitempty"`
	InstanceID string          `json:"instance_id,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	AwaitID    string          `json:"await_id,omitempty"`
	TimeoutMs  int             `json:"timeout_ms,omitempty"`
}

func (r *Router) handleControl(ctx context.Context, conn net.Conn) {
	rd := bufio.NewReader(conn)
	// First-byte demux: JSON (`{`) is a CLI request; anything else
	// is a wash-frame attach from a router-spawned or terminal-
	// launched app. The frame format starts with a flags byte
	// (currently 0x01) — never `{` — so the discriminator is
	// unambiguous.
	first, err := rd.Peek(1)
	if err != nil {
		_ = conn.Close()
		return
	}
	if first[0] != '{' {
		// App attach. The conn lifetime is now owned by the
		// instance loop (or released here on failure).
		r.handleAttach(ctx, conn, rd)
		return
	}
	defer conn.Close()
	line, err := rd.ReadBytes('\n')
	if err != nil {
		return
	}
	var req controlReq
	if err := json.Unmarshal(line, &req); err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": err.Error(),
		})
		return
	}
	switch req.T {
	case "launch":
		r.controlLaunch(ctx, conn, req.AppID)
	case "msg":
		r.controlMsg(ctx, conn, req)
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

// controlMsg relays the JSON `data` payload into the named instance
// as an APP_MSG event. With await_id set, it subscribes for the
// matching reply on the instance's outbound app_msg stream and
// returns it over the socket; without, it acks immediately.
func (r *Router) controlMsg(ctx context.Context, conn net.Conn, req controlReq) {
	if req.InstanceID == "" {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": "missing instance_id",
		})
		return
	}
	if len(req.Data) == 0 {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": "missing data",
		})
		return
	}
	inst := r.appByInstance(req.InstanceID)
	if inst == nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "not_found", "msg": req.InstanceID,
		})
		return
	}
	var parsed any
	if err := json.Unmarshal(req.Data, &parsed); err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": "data is not valid JSON: " + err.Error(),
		})
		return
	}

	// Subscribe BEFORE sending — the BE could reply on a fast loop
	// (in-process tests, synchronous handlers) and we don't want to
	// miss the dispatch.
	var (
		replyCh <-chan map[string]any
		cancel  func()
	)
	if req.AwaitID != "" {
		replyCh, cancel = r.SubscribeAppMsg(req.InstanceID, req.AwaitID)
		defer cancel()
	}

	if err := inst.WriteEvt(wire.NewEvtAppMsg(inst.WindowID, parsed)); err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "internal", "msg": "send: " + err.Error(),
		})
		return
	}

	if replyCh == nil {
		writeControlResponse(conn, map[string]any{"t": "msg.ok"})
		return
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultMsgAwaitTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case data, ok := <-replyCh:
		if !ok {
			// channel closed without delivery — instance died, or
			// a later subscriber to the same matchID stole the slot.
			writeControlResponse(conn, map[string]any{
				"t": "error", "code": "cancelled", "msg": "watcher cancelled",
			})
			return
		}
		writeControlResponse(conn, map[string]any{"t": "msg.reply", "data": data})
	case <-timer.C:
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "timeout", "msg": fmt.Sprintf("no reply for id=%s within %s", req.AwaitID, timeout),
		})
	case <-ctx.Done():
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "shutdown", "msg": "router shutting down",
		})
	}
}

func writeControlResponse(conn net.Conn, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = conn.Write(b)
}
