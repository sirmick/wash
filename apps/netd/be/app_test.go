package netd

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/wiretest"
)

// connectNetd handshakes a Conn against netd's def (running onReady, which wires
// svc/applier) and returns the router pipe end for injecting cross-app app_msgs.
// Mirrors apps/notify/be/app_test.go's connectNotify.
func connectNetd(t *testing.T) (wire.FrameTransport, func()) {
	t.Helper()
	// Force the in-memory fake backend so onReady's selectApplier neither reads
	// the dev's ~/.config/wash/network.json nor probes D-Bus/networkctl — keeps
	// these message-injection tests hermetic.
	t.Setenv("WASH_NETD_BACKEND", "fake")
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
	ack, err := wire.EncodeCtrl(wire.NewIdentityAck("i-netd", 0))
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
	if svc == nil || applier == nil {
		t.Fatal("onReady didn't wire the service")
	}
	go func() { _ = r.c.Run(context.Background()) }()

	return pp.EndB(), func() {
		r.c.Close()
		if timer != nil {
			timer.Stop()
		}
		svc, applier, pending, timer = nil, nil, nil, nil // reset package singletons between tests
		ConfirmTimeout = 90 * time.Second
	}
}

var netSender = wire.Sender{AppID: NetAppID, InstanceID: "i-net"}

var reqSeq int

func nextReqID() string { reqSeq++; return "rq-" + strconv.Itoa(reqSeq) }

// call ships a cross-app request from `from` and returns the decoded reply whose
// req_id matches (skipping state pushes and unrelated frames). Works for both
// _ok and _err replies — inspect data["kind"].
func call(t *testing.T, router wire.FrameTransport, from wire.Sender, kind string, fields map[string]any) map[string]any {
	t.Helper()
	reqID := nextReqID()
	payload := map[string]any{"kind": kind, "req_id": reqID}
	for k, v := range fields {
		payload[k] = v
	}
	ev := wire.NewEvtAppMsgFrom(0, payload, from)
	b, err := wire.EncodeEvt(ev)
	if err != nil {
		t.Fatalf("encode evt: %v", err)
	}
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write evt: %v", err)
	}
	return readWhere(t, router, 2*time.Second, func(d map[string]any) bool { return d["req_id"] == reqID })
}

// readWhere reads channel-1 EvtAppMsg frames until pred matches the decoded data.
func readWhere(t *testing.T, router wire.FrameTransport, timeout time.Duration, pred func(map[string]any) bool) map[string]any {
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
	t.Fatal("no matching reply within timeout")
	return nil
}

// --- config fixtures (FE interchange JSON shape) ---------------------------

func ifaceConfig() map[string]any {
	return map[string]any{
		"Interfaces": []any{
			map[string]any{
				"Name":   "lan",
				"Device": "br-lan",
				"Proto":  map[string]any{"_tag": "static", "IPAddr": []any{"10.0.0.1/24"}},
			},
		},
	}
}

// unknownZoneConfig forwards lan→wan but only declares the lan zone — the
// relational invariant flags Forwardings[0].Dest with code unknown_zone.
func unknownZoneConfig() map[string]any {
	return map[string]any{
		"Zones":       []any{map[string]any{"Name": "lan", "Input": "ACCEPT"}},
		"Forwardings": []any{map[string]any{"Src": "lan", "Dest": "wan"}},
	}
}

func diags(t *testing.T, reply map[string]any) []map[string]any {
	t.Helper()
	raw, ok := reply["diagnostics"].([]any)
	if !ok {
		t.Fatalf("no diagnostics array in reply: %v", reply)
	}
	out := make([]map[string]any, len(raw))
	for i, d := range raw {
		out[i] = d.(map[string]any)
	}
	return out
}

// --- tests ------------------------------------------------------------------

func TestValidateAcceptsValidConfig(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	reply := call(t, router, netSender, "validate", map[string]any{"config": ifaceConfig()})
	if reply["kind"] != "validate_ok" {
		t.Fatalf("kind=%v, want validate_ok", reply["kind"])
	}
	if d := diags(t, reply); len(d) != 0 {
		t.Fatalf("want no diagnostics, got %v", d)
	}
}

func TestValidateFlagsUnknownZone(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	reply := call(t, router, netSender, "validate", map[string]any{"config": unknownZoneConfig()})
	if reply["kind"] != "validate_ok" {
		t.Fatalf("kind=%v, want validate_ok", reply["kind"])
	}
	found := false
	for _, d := range diags(t, reply) {
		if d["code"] == "unknown_zone" {
			found = true
			if d["path"] != "Forwardings[0].Dest" {
				t.Errorf("path=%v, want Forwardings[0].Dest", d["path"])
			}
		}
	}
	if !found {
		t.Fatalf("want unknown_zone diagnostic, got %v", reply["diagnostics"])
	}
}

func TestMutatingRequestForbiddenForForeignSender(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	foreign := wire.Sender{AppID: "com.wash.fm", InstanceID: "i-fm"}
	reply := call(t, router, foreign, "validate", map[string]any{"config": ifaceConfig()})
	if reply["kind"] != "validate_err" {
		t.Fatalf("kind=%v, want validate_err", reply["kind"])
	}
	if reply["code"] != sdk.ErrForbidden {
		t.Fatalf("code=%v, want %q", reply["code"], sdk.ErrForbidden)
	}
}

func TestDiffAgainstLiveBaseline(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	reply := call(t, router, netSender, "diff", map[string]any{"config": ifaceConfig()})
	if reply["kind"] != "diff_ok" {
		t.Fatalf("kind=%v, want diff_ok", reply["kind"])
	}
	entries, _ := reply["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("want 1 diff entry, got %v", reply["entries"])
	}
	e := entries[0].(map[string]any)
	if e["op"] != "add" || e["kind"] != "network/interface" || e["name"] != "lan" {
		t.Fatalf("unexpected entry: %v", e)
	}
}

func TestApplyThenConfirmPromotesLiveConfig(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	reply := call(t, router, netSender, "apply", map[string]any{"config": ifaceConfig()})
	if reply["kind"] != "apply_ok" || reply["state"] != "await-confirm" {
		t.Fatalf("apply reply: %v", reply)
	}
	if got := svc.Snapshot().Status; got != "await-confirm" {
		t.Fatalf("status=%q, want await-confirm", got)
	}
	// Not yet confirmed: live is still empty.
	if len(applier.Live().Interfaces) != 0 {
		t.Fatal("live config changed before confirm")
	}

	cr := call(t, router, netSender, "confirm", nil)
	if cr["kind"] != "confirm_ok" || cr["state"] != "committed" {
		t.Fatalf("confirm reply: %v", cr)
	}
	live := applier.Live()
	if len(live.Interfaces) != 1 || live.Interfaces[0].Name != "lan" {
		t.Fatalf("live not promoted: %+v", live.Interfaces)
	}
	if got := svc.Snapshot().Status; got != "committed" {
		t.Fatalf("status=%q, want committed", got)
	}
}

// TestStatePushCarriesLiveUCI confirms publish() stamps the live config rendered
// to UCI onto the state snapshot — the read-only "Current configuration (UCI)"
// view the Settings Network panel reads from state.uci. Before any config it's
// empty; after a committed apply it carries the rendered interface.
func TestStatePushCarriesLiveUCI(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	if u := svc.Snapshot().UCI; u != "" {
		t.Fatalf("empty live config should render no UCI, got %q", u)
	}

	if reply := call(t, router, netSender, "apply", map[string]any{"config": ifaceConfig()}); reply["kind"] != "apply_ok" {
		t.Fatalf("apply: %v", reply)
	}
	if cr := call(t, router, netSender, "confirm", nil); cr["kind"] != "confirm_ok" {
		t.Fatalf("confirm: %v", cr)
	}

	uci := svc.Snapshot().UCI
	for _, want := range []string{"# /etc/config/network", "config interface 'lan'", "10.0.0.1/24"} {
		if !strings.Contains(uci, want) {
			t.Fatalf("state UCI missing %q:\n%s", want, uci)
		}
	}
}

func TestApplyAutoRevertsOnVerifyFailure(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	applier.(*fakeApplier).setVerifyErr(errors.New("gateway unreachable")) // the lock-out path (§7, §10)

	reply := call(t, router, netSender, "apply", map[string]any{"config": ifaceConfig()})
	if reply["kind"] != "apply_ok" || reply["state"] != "reverted" {
		t.Fatalf("apply reply: %v (want state=reverted)", reply)
	}
	if len(applier.Live().Interfaces) != 0 {
		t.Fatal("live config changed despite verify failure")
	}
	if got := svc.Snapshot().Status; got != "reverted" {
		t.Fatalf("status=%q, want reverted", got)
	}
	// Nothing pending → confirm now errors.
	cr := call(t, router, netSender, "confirm", nil)
	if cr["kind"] != "confirm_err" {
		t.Fatalf("confirm after revert: %v, want confirm_err", cr)
	}
}

func TestApplyRefusesInvalidConfigWithDiagnostics(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	reply := call(t, router, netSender, "apply", map[string]any{"config": unknownZoneConfig()})
	if reply["kind"] != "apply_ok" || reply["state"] != "failed" {
		t.Fatalf("apply reply: %v (want state=failed)", reply)
	}
	if len(diags(t, reply)) == 0 {
		t.Fatal("want diagnostics on a refused apply")
	}
	if len(applier.Live().Interfaces) != 0 {
		t.Fatal("invalid apply must not touch live state")
	}
}

// TestApplyAutoRevertsOnConfirmTimeout is the lock-out protection (docs/NET.md
// §7, §2.9): an applied change that is never confirmed reverts itself, without
// the FE, when the confirm window elapses. Live state is restored and the
// status flips to reverted autonomously.
func TestApplyAutoRevertsOnConfirmTimeout(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()
	ConfirmTimeout = 150 * time.Millisecond // tighten the window for the test

	reply := call(t, router, netSender, "apply", map[string]any{"config": ifaceConfig()})
	if reply["state"] != "await-confirm" {
		t.Fatalf("apply reply: %v", reply)
	}
	// Don't confirm. The window elapses → netd reverts on its own.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Snapshot().Status == "reverted" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := svc.Snapshot().Status; got != "reverted" {
		t.Fatalf("status=%q, want reverted (auto-revert didn't fire)", got)
	}
	if len(applier.Live().Interfaces) != 0 {
		t.Fatal("live config changed despite auto-revert")
	}
	// The pending change is gone → a late confirm errors.
	cr := call(t, router, netSender, "confirm", nil)
	if cr["kind"] != "confirm_err" {
		t.Fatalf("confirm after auto-revert: %v, want confirm_err", cr)
	}
}

// TestStateServiceSnapshotOnSubscribe confirms a subscriber gets the current
// status snapshot, and a subsequent apply pushes an updated state — the contract
// the sidebar gateway (B1d) consumes.
func TestStateServiceSnapshotOnSubscribe(t *testing.T) {
	router, cleanup := connectNetd(t)
	defer cleanup()

	session := wire.Sender{AppID: "com.wash.session", InstanceID: "i-sess"}
	sub := wire.NewEvtAppMsgFrom(0, map[string]any{"kind": "subscribe"}, session)
	b, _ := wire.EncodeEvt(sub)
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: b}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	snap := readWhere(t, router, 2*time.Second, func(d map[string]any) bool { return d["kind"] == "state" })
	st, ok := snap["state"].(map[string]any)
	if !ok || st["status"] != "idle" {
		t.Fatalf("subscribe snapshot: %v", snap)
	}

	// Apply → the subscriber should receive a pushed state with await-confirm.
	// Send raw (not via call) so the state push isn't consumed while hunting
	// for the apply_ok reply — we want to observe the push itself.
	ap := wire.NewEvtAppMsgFrom(0, map[string]any{"kind": "apply", "req_id": "rq-sub-apply", "config": ifaceConfig()}, netSender)
	apb, _ := wire.EncodeEvt(ap)
	if err := router.WriteFrame(wire.Frame{Flags: wire.FlagEnd, Channel: 1, Payload: apb}); err != nil {
		t.Fatalf("write apply: %v", err)
	}
	push := readWhere(t, router, 2*time.Second, func(d map[string]any) bool {
		if d["kind"] != "state" {
			return false
		}
		s, _ := d["state"].(map[string]any)
		return s != nil && s["status"] == "await-confirm"
	})
	st = push["state"].(map[string]any)
	summary, _ := st["summary"].([]any)
	if len(summary) == 0 {
		t.Fatalf("want a non-empty pending summary, got %v", st)
	}
}

// TestProjectSegmentsView locks netd's segment-projection wire view: the segment
// lens runs server-side and the `current` response carries per-segment role +
// carrier + owned-object names (loopback omitted), so the FE groups by name
// against the union-correct Config it already has — no FE-side projection drift.
func TestProjectSegmentsView(t *testing.T) {
	c := model.Config{
		Interfaces: []model.Interface{
			{Name: "loopback", Device: "lo", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}},
			{Name: "iot", Device: "iot.6", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("192.168.15.1/24")}}},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{IPv4: true}},
		},
		Devices: []model.Device{{Name: "iot.6", Type: "8021q", Ifname: "switch", VID: 6}},
		Zones: []model.Zone{
			{Name: "iot", Networks: []string{"iot"}, Input: "ACCEPT", Forward: "REJECT"},
			{Name: "wan", Networks: []string{"wan"}, Masq: true},
		},
		Pools: []model.DHCPPool{{Name: "iot", Interface: "iot", Start: 50, Limit: 150}},
	}
	segs := projectSegments(c)
	if len(segs) != 2 {
		t.Fatalf("want 2 segments (loopback omitted), got %d: %+v", len(segs), segs)
	}
	by := map[string]segmentDTO{}
	for _, s := range segs {
		by[s.Name] = s
	}
	iot := by["iot"]
	if iot.Role != "lan" {
		t.Errorf("iot role = %q, want lan", iot.Role)
	}
	if iot.Carrier.Kind != "vlan" || iot.Carrier.VID != 6 || iot.Carrier.Port != "switch" {
		t.Errorf("iot carrier = %+v, want vlan 6 on switch", iot.Carrier)
	}
	if iot.Device != "iot.6" || iot.Zone != "iot" || iot.Pool != "iot" {
		t.Errorf("iot ownership wrong: device=%q zone=%q pool=%q", iot.Device, iot.Zone, iot.Pool)
	}
	if len(iot.Addrs) != 1 || iot.Addrs[0] != "192.168.15.1/24" {
		t.Errorf("iot addrs = %v, want [192.168.15.1/24]", iot.Addrs)
	}
	if by["wan"].Role != "wan" {
		t.Errorf("wan role = %q, want wan", by["wan"].Role)
	}
}
