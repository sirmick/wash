package net

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// connectNet handshakes a Conn against net's def (running onReady, which
// registers the proxy handlers) and returns the router pipe end. The test plays
// BOTH roles over this one pipe: the FE (inject own-FE requests, read the
// relayed replies) and com.wash.netd (read the forwarded request, reply).
func connectNet(t *testing.T) (wire.FrameTransport, func()) {
	t.Helper()
	pp := wiretest.NewPipePair()

	type res struct {
		c   *sdk.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := sdk.ConnectWith(pp.EndA(), def); ch <- res{c, err} }()

	if _, err := pp.EndB().ReadFrame(); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	ack, err := wire.EncodeCtrl(wire.NewIdentityAck("i-net", 1))
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	if err := pp.EndB().WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 0, Payload: ack}); err != nil {
		t.Fatalf("write ack: %v", err)
	}

	r := <-ch
	if r.err != nil {
		t.Fatalf("connect: %v", r.err)
	}
	go func() { _ = r.c.Run(context.Background()) }()
	return pp.EndB(), func() { r.c.Close() }
}

// writeFEReq injects an own-FE request (no router-attested From), as the shell
// delivers a window's app_msg.
func writeFEReq(t *testing.T, router wire.FrameTransport, kind, id string, fields map[string]any) {
	t.Helper()
	payload := map[string]any{"kind": kind, "id": id}
	for k, v := range fields {
		payload[k] = v
	}
	writeEvt(t, router, wire.NewEvtAppMsg(0, payload))
}

// replyAsNetd ships a reply as the com.wash.netd service would — addressed back
// with the hop req_id the proxy is awaiting in sdk.Call.
func replyAsNetd(t *testing.T, router wire.FrameTransport, reqID string, fields map[string]any) {
	t.Helper()
	payload := map[string]any{"req_id": reqID}
	for k, v := range fields {
		payload[k] = v
	}
	writeEvt(t, router, wire.NewEvtAppMsgFrom(0, payload, wire.Sender{AppID: NetdAppID, InstanceID: "i-netd"}))
}

func writeEvt(t *testing.T, router wire.FrameTransport, ev wire.EvtAppMsg) {
	t.Helper()
	b, err := wire.EncodeEvt(ev)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
}

// readData reads channel-1 EvtAppMsg frames until pred matches the decoded data.
func readData(t *testing.T, router wire.FrameTransport, timeout time.Duration, pred func(map[string]any) bool) map[string]any {
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
			t.Fatalf("decode evt: %v", err)
		}
		am, ok := m.(wire.EvtAppMsg)
		if !ok {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(am.Data, &data); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		if pred(data) {
			return data
		}
	}
	t.Fatal("no matching frame within timeout")
	return nil
}

func ifaceConfig() map[string]any {
	return map[string]any{
		"Interfaces": []any{
			map[string]any{"Name": "lan", "Proto": map[string]any{"_tag": "static", "IPAddr": []any{"10.0.0.1/24"}}},
		},
	}
}

// TestProxyForwardsAndRelaysReply is the core round-trip: an FE validate request
// is forwarded to com.wash.netd (config intact, hop req_id minted), and netd's
// reply is relayed back to the FE with the FE id echoed and the hop req_id
// stripped.
func TestProxyForwardsAndRelaysReply(t *testing.T) {
	router, cleanup := connectNet(t)
	defer cleanup()

	writeFEReq(t, router, "validate", "f-1", map[string]any{"config": ifaceConfig()})

	fwd := readData(t, router, 2*time.Second, func(d map[string]any) bool {
		return d["kind"] == "validate" && d["req_id"] != nil
	})
	if fwd["config"] == nil {
		t.Fatalf("config not forwarded to netd: %v", fwd)
	}
	if fwd["id"] != nil {
		t.Errorf("FE envelope id leaked into the netd forward: %v", fwd["id"])
	}
	reqID := fwd["req_id"].(string)

	replyAsNetd(t, router, reqID, map[string]any{
		"kind": "validate_ok",
		"diagnostics": []any{
			map[string]any{"path": "Interfaces[0].Proto.IPAddr", "code": "bad", "message": "nope", "severity": "error"},
		},
	})

	rep := readData(t, router, 2*time.Second, func(d map[string]any) bool { return d["kind"] == "validate_ok" })
	if rep["id"] != "f-1" {
		t.Errorf("FE reply id=%v, want f-1", rep["id"])
	}
	if rep["req_id"] != nil {
		t.Errorf("hop req_id leaked back to the FE: %v", rep["req_id"])
	}
	ds, _ := rep["diagnostics"].([]any)
	if len(ds) != 1 {
		t.Fatalf("diagnostics not relayed: %v", rep["diagnostics"])
	}
}

// TestProxyRelaysError confirms a netd _err reply reaches the FE as <kind>_err
// with code/msg and the echoed id.
func TestProxyRelaysError(t *testing.T) {
	router, cleanup := connectNet(t)
	defer cleanup()

	writeFEReq(t, router, "apply", "f-2", map[string]any{"config": ifaceConfig()})
	fwd := readData(t, router, 2*time.Second, func(d map[string]any) bool {
		return d["kind"] == "apply" && d["req_id"] != nil
	})
	replyAsNetd(t, router, fwd["req_id"].(string), map[string]any{
		"kind": "apply_err", "code": sdk.ErrForbidden, "msg": "denied",
	})

	rep := readData(t, router, 2*time.Second, func(d map[string]any) bool { return d["kind"] == "apply_err" })
	if rep["id"] != "f-2" || rep["code"] != sdk.ErrForbidden || rep["msg"] != "denied" {
		t.Fatalf("error reply not relayed faithfully: %v", rep)
	}
}

// TestProxyConfirmCarriesNoConfig confirms confirm/revert forward with no config
// payload (only the kind matters).
func TestProxyConfirmCarriesNoConfig(t *testing.T) {
	router, cleanup := connectNet(t)
	defer cleanup()

	writeFEReq(t, router, "confirm", "f-3", nil)
	fwd := readData(t, router, 2*time.Second, func(d map[string]any) bool {
		return d["kind"] == "confirm" && d["req_id"] != nil
	})
	if _, has := fwd["config"]; has {
		t.Errorf("confirm should not carry a config: %v", fwd)
	}
	replyAsNetd(t, router, fwd["req_id"].(string), map[string]any{"kind": "confirm_ok", "state": "committed"})

	rep := readData(t, router, 2*time.Second, func(d map[string]any) bool { return d["kind"] == "confirm_ok" })
	if rep["id"] != "f-3" || rep["state"] != "committed" {
		t.Fatalf("confirm reply not relayed: %v", rep)
	}
}
