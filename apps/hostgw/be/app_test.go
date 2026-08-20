package hostgw

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// connectHostgw mirrors apps/notify/be/app_test.go's connectNotify: bind
// the gateway's onReady to a freshly-handshaken Conn over an in-memory
// pipe, and hand the test the router-side end for injecting frames.
//
// The router end MUST be read by the test — onReady writes one subscribe
// per watched service the moment it runs, and every republish is another
// outbound frame. Helpers below (readAppMsg, drainStartupSubscribes) keep
// the queue moving.
func connectHostgw(t *testing.T) (wire.FrameTransport, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()

	type connResult struct {
		c   *sdk.Conn
		err error
	}
	ch := make(chan connResult, 1)
	go func() {
		c, err := sdk.ConnectWith(pp.EndA(), def)
		ch <- connResult{c, err}
	}()

	// Drain the identity frame and complete the handshake, same shape as
	// the notify service's test.
	if _, err := pp.EndB().ReadFrame(); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	ackPayload, err := wire.EncodeCtrl(wire.NewIdentityAck("i-hostgw", 0))
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	if err := pp.EndB().WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: ackPayload}); err != nil {
		t.Fatalf("write ack: %v", err)
	}

	res := <-ch
	if res.err != nil {
		t.Fatalf("connect: %v", res.err)
	}
	go func() { _ = res.c.Run(context.Background()) }()

	cleanup := func() {
		res.c.Close()
		// The cache is package-level (onReady fires once per process under
		// singleton instancing), so it outlives one test in this package.
		cache.reset()
	}
	return pp.EndB(), cleanup
}

// writeServiceState injects an app_msg{kind:"state"} as if the router had
// relayed one from the named service, with the sender attested.
func writeServiceState(t *testing.T, router wire.FrameTransport, fromAppID string, state any) {
	t.Helper()
	payload := map[string]any{"kind": "state", "state": state}
	msg := wire.NewEvtAppMsgFrom(0, payload, wire.Sender{AppID: fromAppID, InstanceID: "i-" + fromAppID})
	writeEvt(t, router, msg)
}

// writeShellSubscribe injects the shell's catch-up request. Deliberately
// UNATTESTED (no From): that is exactly how the router relays a
// shell-originated cross-app send (handleAppMsgSend builds a plain
// EvtAppMsg), and the point of this test is that hostgw answers it.
func writeShellSubscribe(t *testing.T, router wire.FrameTransport) {
	t.Helper()
	writeEvt(t, router, wire.NewEvtAppMsg(0, map[string]any{"kind": "subscribe"}))
}

func writeEvt(t *testing.T, router wire.FrameTransport, msg wire.EvtAppMsg) {
	t.Helper()
	b, err := wire.EncodeEvt(msg)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// readAppMsgWhere reads frames until an EvtAppMsg satisfying want
// appears. Everything else is dropped, which is what lets a test ignore
// the startup subscribe burst without ordering assumptions.
func readAppMsgWhere(t *testing.T, router wire.FrameTransport, timeout time.Duration, what string, want func(wire.EvtAppMsg) bool) wire.EvtAppMsg {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := router.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Channel != 1 {
			continue
		}
		m, err := wire.DecodeEvt(f.Payload)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		msg, ok := m.(wire.EvtAppMsg)
		if ok && want(msg) {
			return msg
		}
	}
	t.Fatalf("no %s arrived within %v", what, timeout)
	return wire.EvtAppMsg{}
}

// isRepublish matches an FE-bound frame (To == nil) carrying our
// envelope kind. Outbound subscribes to services carry a recipient, so
// To is the discriminator.
func isRepublish(msg wire.EvtAppMsg) bool {
	if msg.To != nil {
		return false
	}
	env, err := decodeEnvelope(msg)
	if err != nil {
		return false
	}
	return env["kind"] == FEKind
}

func decodeEnvelope(msg wire.EvtAppMsg) (map[string]any, error) {
	var env map[string]any
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		return nil, err
	}
	return env, nil
}

// readRepublish waits for the next FE-bound hostgw.state frame.
func readRepublish(t *testing.T, router wire.FrameTransport) map[string]any {
	t.Helper()
	msg := readAppMsgWhere(t, router, 2*time.Second, "hostgw.state republish", isRepublish)
	env, err := decodeEnvelope(msg)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

// waitFor polls fn until it returns true or the deadline expires.
func waitFor(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", label)
}

// TestHostgwSubscribesToEveryWatchedService is the guard on the whole
// point of the app: at startup it must ask each of its host's services
// for state. A service dropped from `watched` silently loses a rail
// section on every host, with nothing else failing.
func TestHostgwSubscribesToEveryWatchedService(t *testing.T) {
	router, cleanup := connectHostgw(t)
	defer cleanup()

	got := map[string]bool{}
	for range watched {
		msg := readAppMsgWhere(t, router, 2*time.Second, "startup subscribe", func(m wire.EvtAppMsg) bool {
			return m.To != nil && m.To.AppID != ""
		})
		env, err := decodeEnvelope(msg)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env["kind"] != "subscribe" {
			t.Fatalf("startup send to %s has kind=%v, want subscribe", msg.To.AppID, env["kind"])
		}
		got[msg.To.AppID] = true
	}
	for _, appID := range watched {
		if !got[appID] {
			t.Errorf("no startup subscribe addressed to %s", appID)
		}
	}
}

// TestHostgwRepublishesServiceState is the load-bearing hop: a service's
// state push comes back out on the FE-bound path, tagged with the
// service name and with the state body untouched. That FE-bound frame is
// what relayAppMsgToShell fans to every attached shell — including A's
// tunnelled one.
func TestHostgwRepublishesServiceState(t *testing.T) {
	router, cleanup := connectHostgw(t)
	defer cleanup()

	// A nested body, so "verbatim" means something: a flat string would
	// pass even if we re-encoded through a typed struct.
	state := map[string]any{
		"notifications": []any{
			map[string]any{"id": "7", "title": "build finished", "level": "info"},
		},
	}
	writeServiceState(t, router, NotifyAppID, state)

	env := readRepublish(t, router)
	if env["service"] != "notify" {
		t.Fatalf("service=%v, want notify", env["service"])
	}
	body, ok := env["state"].(map[string]any)
	if !ok {
		t.Fatalf("state shape: %T", env["state"])
	}
	notes, ok := body["notifications"].([]any)
	if !ok || len(notes) != 1 {
		t.Fatalf("notifications=%v", body["notifications"])
	}
	n := notes[0].(map[string]any)
	if n["title"] != "build finished" || n["id"] != "7" {
		t.Fatalf("state body was not forwarded verbatim: %v", n)
	}
}

// TestHostgwLateSubscribeReplaysEverything is the case the plan calls out
// as the interesting one: a shell that attaches AFTER the state changed
// must still see it. Nothing re-pushes on its own — a quiescent pending
// priv approval produces no further events — so the replay is the only
// thing standing between a late shell and an empty rail.
func TestHostgwLateSubscribeReplaysEverything(t *testing.T) {
	router, cleanup := connectHostgw(t)
	defer cleanup()

	writeServiceState(t, router, NotifyAppID, map[string]any{"notifications": []any{}})
	writeServiceState(t, router, AgentdAppID, map[string]any{"rows": []any{}})
	// Both republishes (to the shells attached at the time) come out
	// first; drain them so the replay below is unambiguous.
	readRepublish(t, router)
	readRepublish(t, router)
	waitFor(t, "both services cached", func() bool { return cache.len() == 2 })

	writeShellSubscribe(t, router)

	// One republish per cached service, in name order (cache.all sorts).
	first := readRepublish(t, router)
	second := readRepublish(t, router)
	if first["service"] != "agent" || second["service"] != "notify" {
		t.Fatalf("replay = [%v, %v], want [agent, notify]", first["service"], second["service"])
	}
}

// TestHostgwIgnoresUnknownSender pins the trust rule: the service name
// comes from the router-attested sender app id, so an app we do not
// gateway cannot inject state under a name the rail trusts. If this
// regressed, any app on the host could forge (say) priv's pending count.
func TestHostgwIgnoresUnknownSender(t *testing.T) {
	router, cleanup := connectHostgw(t)
	defer cleanup()

	writeServiceState(t, router, "com.wash.impostor", map[string]any{"queue": []any{"forged"}})
	// Then a legitimate one, so we have a positive edge to wait for
	// rather than sleeping on the absence of a frame.
	writeServiceState(t, router, PrivAppID, map[string]any{"queue": []any{}})

	env := readRepublish(t, router)
	if env["service"] != "priv" {
		t.Fatalf("first republish is service=%v — the impostor was forwarded", env["service"])
	}
	if got := cache.len(); got != 1 {
		t.Fatalf("cache holds %d entries, want 1 (the impostor was cached)", got)
	}
}

// TestServiceName pins the app-id → service-name mapping. A typo here
// silently drops a service's pushes (serviceName returns "" → the
// handler ignores them), which is the same failure mode
// apps/session/be's TestServiceFEKind guards against.
func TestServiceName(t *testing.T) {
	cases := map[string]string{
		NotifyAppID:        "notify",
		BulkAppID:          "bulk",
		PrivAppID:          "priv",
		NetdAppID:          "net",
		AudioAppID:         "audio",
		AgentdAppID:        "agent",
		"com.wash.remote":  "", // already host-aware; not ours to republish
		"com.wash.unknown": "",
	}
	for appID, want := range cases {
		if got := serviceName(appID); got != want {
			t.Errorf("serviceName(%q) = %q, want %q", appID, got, want)
		}
	}
	// And every watched app id must map to a name, or startup subscribes
	// to something whose pushes we then drop on the floor.
	for _, appID := range watched {
		if serviceName(appID) == "" {
			t.Errorf("watched app %q has no service name", appID)
		}
	}
}
