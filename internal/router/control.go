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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirmick/wash/pkg/wire"
)

// ResolveControlSocket settles WHICH control-socket path this router will
// own, before anything is spawned.
//
// It must happen this early because the path is handed to every app the
// router starts (WASH_DISPLAY), and background apps come up during
// bring-up — deciding inside ListenControl left a window where an app
// could be told the path we were about to step away from, and it would
// then attach to the OTHER router.
//
// Returns path unchanged when it is free or merely stale; returns a
// per-pid sibling when a live router is already answering there.
func ResolveControlSocket(path string, log Logger) string {
	if path == "" || !peerAlive(path) {
		return path
	}
	alt := strings.TrimSuffix(path, ".sock") + "." + strconv.Itoa(os.Getpid()) + ".sock"
	if log != nil {
		log("control socket %s is owned by a live router — using %s instead", path, alt)
	}
	return alt
}

// peerAlive reports whether something is still accepting on this unix
// socket path — i.e. whether taking the name would steal it from a live
// owner. A refused connection (or no file at all) means the path is
// stale and safe to rebind.
//
// The dial is immediately closed: the control protocol tolerates a
// connection that says nothing, and this asks a question about the
// listener, not about the protocol.
func peerAlive(path string) bool {
	c, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

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
	// Never steal a socket somebody is still answering on.
	//
	// The default path is per-UID (/tmp/wash-<uid>.sock), and since
	// LIFETIME a user routinely has several routers alive at once — an
	// old session outliving its browser, a dev router, an e2e run. The
	// unlink-then-bind below used to make the newest router the owner,
	// and every app the OTHER routers spawned then dialled it, failed the
	// registered-binary check, and timed out with "binary does not match
	// registered app_id". Observed on a real desktop, where it broke app
	// launching for a session that had done nothing wrong.
	//
	// A socket that ACCEPTS a connection has a live owner, so we step
	// aside onto a per-pid path instead. Everything the router spawns
	// learns the path from WASH_DISPLAY, so its apps are unaffected; what
	// is lost is a bare `wash launch` from an unrelated shell finding
	// THIS router by the well-known name — which is the right thing to
	// lose, because that name belongs to whoever got there first.
	// Normally a no-op: the runner resolved this before bring-up, so the
	// path is already ours. Kept because ListenControl is callable on its
	// own (tests, embedders), and stepping aside is always safer than
	// stealing.
	path = ResolveControlSocket(path, r.log)
	// Best-effort: remove any stale socket from a previous run. Stale by
	// now means "nothing answered on it", checked immediately above.
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
	// Deliberately NOT written back to r.cfg: spawns read that field from
	// other goroutines, so mutating it here would be a data race for the
	// benefit of a case the runner has already handled — it resolves the
	// path before the Router is built, precisely so this value is settled
	// before anything can be spawned. A direct caller that lands on the
	// fallback gets it in the log line below.
	//
	// Remember WHICH FILE it is. On shutdown we unlink only if the
	// path still names this socket: unlinking by name alone is how a
	// router that exits deletes a NEWER router's socket, leaving that one
	// listening on an inode nothing can reach.
	mine, statErr := os.Stat(path)

	r.log("control socket listening on %s", path)
	go func() {
		<-ctx.Done()
		_ = lis.Close()
		if statErr != nil {
			return
		}
		if now, err := os.Stat(path); err == nil && os.SameFile(mine, now) {
			_ = os.Remove(path)
		}
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

// controlHeader is the tiny dispatch peek: the only field
// handleControl needs to pick a verb. The full line bytes are then
// re-decoded into the verb-specific request type — cheap, and it
// keeps each protocol's fields out of the others' structs.
type controlHeader struct {
	T string `json:"t"`
}

// launchMsgReq carries the fields for the simple `launch` / `msg`
// verbs. (launch uses only AppID; msg uses the rest.)
type launchMsgReq struct {
	AppID      string          `json:"app_id,omitempty"`
	InstanceID string          `json:"instance_id,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	AwaitID    string          `json:"await_id,omitempty"`
	TimeoutMs  int             `json:"timeout_ms,omitempty"`
}

// privRunReq carries the fields for priv.run (wash-sudo) streaming.
// The control handler promotes the connection to a streaming
// cliSession and routes these into wash-priv as a normal cross-app
// message. The follow-up frames (priv.stdin / priv.cancel / ...) on
// the upgraded conn reuse this type — they only populate T and
// StreamBytes.
type privRunReq struct {
	T           string            `json:"t"`
	AppID       string            `json:"app_id,omitempty"`
	ReqID       string            `json:"req_id,omitempty"`
	Argv        []string          `json:"argv,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Window      bool              `json:"window,omitempty"`
	NoPrompt    bool              `json:"no_prompt,omitempty"`
	StreamBytes string            `json:"bytes,omitempty"` // priv.stdin, base64
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
	// Two-phase decode: peek `t` to pick the verb, then re-decode the
	// same bytes into the verb-specific request struct.
	var hdr controlHeader
	if err := json.Unmarshal(line, &hdr); err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": err.Error(),
		})
		return
	}
	switch hdr.T {
	case "launch":
		var req launchMsgReq
		if err := json.Unmarshal(line, &req); err != nil {
			writeControlResponse(conn, map[string]any{
				"t": "error", "code": "bad_request", "msg": err.Error(),
			})
			return
		}
		r.controlLaunch(ctx, conn, req.AppID)
	case "msg":
		var req launchMsgReq
		if err := json.Unmarshal(line, &req); err != nil {
			writeControlResponse(conn, map[string]any{
				"t": "error", "code": "bad_request", "msg": err.Error(),
			})
			return
		}
		r.controlMsg(ctx, conn, req)
	case "priv.run":
		var req privRunReq
		if err := json.Unmarshal(line, &req); err != nil {
			writeControlResponse(conn, map[string]any{
				"t": "error", "code": "bad_request", "msg": err.Error(),
			})
			return
		}
		// priv.run upgrades the conn to a streaming session — do NOT
		// defer close here, the handler owns the lifetime now.
		r.controlPrivRun(ctx, conn, rd, req)
		return
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
	// stomp on the autoboot session; refuse. Background services
	// take a different path: they autoboot on shell connect, but
	// launching via control is allowed because they're singleton-
	// instancing (resolveRecipient will return the existing one if
	// already running, spawn on demand if not). Useful for headless
	// e2e harnesses that drive the wire surface without a browser.
	if entry.Manifest.Surface == SurfaceDesktop {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "forbidden", "msg": "cannot launch desktop-surface app via control socket",
		})
		return
	}
	if entry.Manifest.Surface == SurfaceBackground {
		// Route through resolveRecipient so the singleton table is
		// consulted (returns existing instance) before spawning.
		inst, code, err := r.resolveRecipient(ctx, wire.Recipient{AppID: appID})
		if err != nil {
			writeControlResponse(conn, map[string]any{
				"t": "error", "code": code, "msg": err.Error(),
			})
			return
		}
		writeControlResponse(conn, map[string]any{
			"t":           "launched",
			"instance_id": inst.InstanceID,
			"window_id":   uint64(inst.WindowID),
		})
		return
	}
	inst, err := r.launchOrRaise(ctx, entry)
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
func (r *Router) controlMsg(ctx context.Context, conn net.Conn, req launchMsgReq) {
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
	parsed, err := decodeControlData(req.Data)
	if err != nil {
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

// controlPrivRun is the wash-sudo bridge. The control conn becomes a
// streaming cliSession; the initial priv.run request is dispatched
// to wash-priv as a cross-app message with from = the cli session;
// further JSON lines from wash-sudo (priv.stdin, priv.cancel, ...)
// are forwarded as additional app_msgs to wash-priv.
//
// Lifetime: returns when wash-sudo closes the conn OR wash-priv has
// finished the request (signalled by writing the priv.result back
// out, which wash-priv triggers via its stream → cliSession.writeJSON
// path). The handler closes the conn on return — the outer
// defer in handleControl actually does this.
func (r *Router) controlPrivRun(ctx context.Context, conn net.Conn, rd *bufio.Reader, first privRunReq) {
	// Bare-invocation heuristic: `wash-sudo wash-term` (one
	// positional, no flags, basename matches a registered app's
	// binary) auto-promotes to spawn mode. Saves the user from
	// having to know wash-priv's flag surface — wash-term IS a
	// wash app, treat it as one.
	if first.AppID == "" && !first.Window && len(first.Argv) == 1 {
		if entry := r.reg.FindByBasename(first.Argv[0]); entry != nil {
			first.AppID = entry.Manifest.ID
			first.Argv = nil
			r.log("priv.run heuristic: %s → spawn app_id=%s", entry.Path, first.AppID)
		}
	}
	// One ops-friendly log line per CLI invocation. Audit details
	// land in wash-priv's audit log; this is just "router saw a
	// wash-sudo invocation arrive."
	r.log("priv.run req_id=%s argv=%v app_id=%s", first.ReqID, first.Argv, first.AppID)
	if first.ReqID == "" {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": "bad_request", "msg": "priv.run requires req_id",
		})
		return
	}
	// Resolve the wash-priv singleton (spawning on demand).
	target, code, err := r.resolveRecipient(ctx, wire.Recipient{AppID: "com.wash.priv"})
	if err != nil {
		writeControlResponse(conn, map[string]any{
			"t": "error", "code": code, "msg": err.Error(),
		})
		return
	}

	session := r.registerCliSession(conn)
	defer r.unregisterCliSession(session)

	// Ack the registration to wash-sudo so it knows the request has
	// at least reached the router. Subsequent traffic from wash-priv
	// (priv.event envelopes) lands on the same conn via writeJSON.
	if err := session.writeJSON(map[string]any{
		"t":            "priv.registered",
		"req_id":       first.ReqID,
		"cli_instance": session.instanceID,
	}); err != nil {
		return
	}

	// Build the payload kind from the priv.run flags:
	//
	//   AppID set        → kind:"spawn" (launch a registered wash app
	//                       as root via PrepareSpawn — long-running UI)
	//   Window + argv    → kind:"run"   (wash-term --exec argv as root,
	//                       new terminal window with the output)
	//   plain argv       → kind:"run_inline" (sudo argv with streamed
	//                       stdin/stdout/stderr back to wash-sudo)
	//
	// cli_origin carries router-attested peer info (pid + uid +
	// resolved tty + comm). The fields are derived from
	// SO_PEERCRED + /proc, so wash-sudo can't spoof them — by the
	// time the payload reaches wash-priv, the values are the
	// router's view, not anything the client wrote.
	info := readProcInfo(session.peerPID)
	originMap := map[string]any{
		"pid":  int64(info.PID),
		"uid":  int64(info.UID),
		"comm": info.Comm,
		"tty":  info.TTY,
	}
	var payload map[string]any
	switch {
	case first.AppID != "":
		payload = map[string]any{
			"kind":       "spawn",
			"req_id":     first.ReqID,
			"app_id":     first.AppID,
			"args":       first.Argv,
			"reason":     first.Reason,
			"cli_origin": originMap,
		}
	case first.Window:
		payload = map[string]any{
			"kind":       "run",
			"req_id":     first.ReqID,
			"argv":       first.Argv,
			"reason":     first.Reason,
			"cli_origin": originMap,
		}
	default:
		payload = map[string]any{
			"kind":       "run_inline",
			"req_id":     first.ReqID,
			"argv":       first.Argv,
			"cwd":        first.Cwd,
			"reason":     first.Reason,
			"env":        first.Env,
			"no_prompt":  first.NoPrompt,
			"cli_origin": originMap,
		}
	}
	from := wire.Sender{AppID: "cli.wash.sudo", InstanceID: session.instanceID}
	if err := target.WriteEvt(wire.NewEvtAppMsgFrom(target.WindowID, payload, from)); err != nil {
		_ = session.writeJSON(map[string]any{
			"t": "priv.error", "req_id": first.ReqID, "code": "internal", "msg": err.Error(),
		})
		return
	}

	// Loop reading further JSON lines from wash-sudo. priv.stdin
	// (and priv.cancel) get forwarded as app_msgs to wash-priv on
	// the same session. EOF on the conn = wash-sudo went away; the
	// defer'd unregister tells wash-priv (via the next write-attempt
	// failing) that the back-channel is gone.
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			// Notify wash-priv that the CLI side hung up so it can
			// kill any in-flight subprocess. If even that fails, the
			// subprocess may run on unsupervised — worth a line.
			if werr := target.WriteEvt(wire.NewEvtAppMsgFrom(target.WindowID,
				map[string]any{"kind": "cli.disconnect", "req_id": first.ReqID},
				from)); werr != nil {
				r.log("priv.run req_id=%s: cli.disconnect to wash-priv lost: %v", first.ReqID, werr)
			}
			return
		}
		var next privRunReq
		if err := json.Unmarshal(line, &next); err != nil {
			continue // tolerate junk lines rather than tear the session down
		}
		var fwd map[string]any
		switch next.T {
		case "priv.stdin":
			fwd = map[string]any{
				"kind":   "stdin",
				"req_id": first.ReqID,
				"bytes":  next.StreamBytes, // base64; wash-priv decodes
			}
		case "priv.stdin.close":
			fwd = map[string]any{"kind": "stdin_close", "req_id": first.ReqID}
		case "priv.cancel":
			fwd = map[string]any{"kind": "cancel", "req_id": first.ReqID}
		}
		if fwd == nil {
			continue
		}
		// A lost forward leaves wash-sudo's caller hanging (stdin that
		// never arrives, a cancel that never lands) with no trace.
		if werr := target.WriteEvt(wire.NewEvtAppMsgFrom(target.WindowID, fwd, from)); werr != nil {
			r.log("priv.run req_id=%s: forward %s to wash-priv lost: %v", first.ReqID, next.T, werr)
		}
	}
}

// decodeControlData unmarshals the `data` payload from a control-socket
// msg request, preserving integer-ness of numeric fields.
//
// json.Unmarshal into `any` turns every JSON number into float64.
// Bus handlers re-marshal through json.Marshal + Unmarshal into the
// typed Req struct; an integer-typed field (e.g. signalReq.PID int32)
// rejects a fractional-looking float. Using json.Decoder with
// UseNumber() preserves the original token, and we then normalize:
// integer-valued numbers become int64, fractional ones become float64.
// The bus's typed decode then accepts both faithfully.
func decodeControlData(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return normalizeNumbers(v), nil
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case map[string]any:
		for k, vv := range x {
			x[k] = normalizeNumbers(vv)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = normalizeNumbers(vv)
		}
		return x
	default:
		return v
	}
}
