package router

import (
	"slices"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// TestRelayedRouterRefusesNestedPeer proves the no-nesting guard: a router that
// is itself a relay endpoint (Config.Relayed — started with --listen-raw, the
// "host B" of docs/REMOTE.md) must refuse to register a further peer, so a
// B→C second hop can never be established. A normal (non-relayed) router still
// registers. This is the hard backstop behind wash-connect's up-front refusal.
func TestRelayedRouterRefusesNestedPeer(t *testing.T) {
	reg := NewRegistry()

	relayed := NewRouter(Config{NoSession: true, Relayed: true}, reg, func(f string, a ...any) { t.Logf("relayed: "+f, a...) })
	inst := &AppInstance{AppID: remoteSupervisorAppID, InstanceID: "i-remote", router: relayed}
	if err := inst.handlePeerRegister(wire.EvtPeerRegister{Origin: "C", Network: "unix", Addr: "/tmp/c.sock"}); err != nil {
		t.Fatalf("handlePeerRegister(relayed): %v", err)
	}
	relayed.peersMu.Lock()
	_, nested := relayed.peers["C"]
	relayed.peersMu.Unlock()
	if nested {
		t.Fatal("relayed endpoint registered a nested peer — wash connections must not nest")
	}

	// Control: a top-level router registers the same peer fine.
	top := NewRouter(Config{NoSession: true}, reg, func(string, ...any) {})
	inst2 := &AppInstance{AppID: remoteSupervisorAppID, InstanceID: "i-remote", router: top}
	if err := inst2.handlePeerRegister(wire.EvtPeerRegister{Origin: "C", Network: "unix", Addr: "/tmp/c.sock"}); err != nil {
		t.Fatalf("handlePeerRegister(top): %v", err)
	}
	top.peersMu.Lock()
	_, ok := top.peers["C"]
	top.peersMu.Unlock()
	if !ok {
		t.Fatal("top-level router should register peer C")
	}
}

// TestSpawnEnvMarksRelayed proves spawned apps learn they are on a relayed
// desktop via WASH_RELAYED=1 (how wash-connect decides to refuse up front),
// and that a normal router does not set it.
func TestSpawnEnvMarksRelayed(t *testing.T) {
	reg := NewRegistry()
	relayed := NewRouter(Config{NoSession: true, Relayed: true}, reg, func(string, ...any) {})
	if !slices.Contains(relayed.spawnEnv(), "WASH_RELAYED=1") {
		t.Fatal("relayed router must export WASH_RELAYED=1 to spawned apps")
	}
	top := NewRouter(Config{NoSession: true}, reg, func(string, ...any) {})
	if slices.Contains(top.spawnEnv(), "WASH_RELAYED=1") {
		t.Fatal("non-relayed router must not set WASH_RELAYED")
	}
}
