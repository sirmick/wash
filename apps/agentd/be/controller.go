package agentd

import (
	"bytes"
	"encoding/json"
	"log"
	"sync"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// A hosted ACP session has zero or one controlling Agent window. Other
// consumers (notably editor tabs) may still watch its transcript, but they do
// not become the target of Focus and cannot cause a second controller window.
var controllerState = struct {
	sync.Mutex
	byKey      map[string]string
	byInstance map[string]string
	launching  map[string]bool
	managers   map[string]struct{}
}{byKey: map[string]string{}, byInstance: map[string]string{}, launching: map[string]bool{}, managers: map[string]struct{}{}}

var controllerConn *sdk.Conn

func claimController(key, instance string) (string, bool) {
	if key == "" || instance == "" {
		return "", false
	}
	controllerState.Lock()
	defer controllerState.Unlock()
	if owner := controllerState.byKey[key]; owner != "" && owner != instance {
		return owner, false
	}
	if old := controllerState.byInstance[instance]; old != "" && old != key {
		delete(controllerState.byKey, old)
	}
	controllerState.byKey[key] = instance
	controllerState.byInstance[instance] = key
	delete(controllerState.launching, key)
	return instance, true
}

func controllerFor(key string) string {
	controllerState.Lock()
	defer controllerState.Unlock()
	return controllerState.byKey[key]
}

func reserveControllerLaunch(key string) bool {
	controllerState.Lock()
	defer controllerState.Unlock()
	if controllerState.byKey[key] != "" || controllerState.launching[key] {
		return false
	}
	controllerState.launching[key] = true
	return true
}

func clearControllerLaunch(key string) {
	controllerState.Lock()
	delete(controllerState.launching, key)
	controllerState.Unlock()
}

func releaseController(instance string) string {
	controllerState.Lock()
	defer controllerState.Unlock()
	key := controllerState.byInstance[instance]
	if key != "" {
		delete(controllerState.byInstance, instance)
		if controllerState.byKey[key] == instance {
			delete(controllerState.byKey, key)
		}
	}
	return key
}

func sessionView(state State, key string) State {
	out := State{}
	for _, r := range state.Rows {
		if r.Key == key {
			out.Rows = []Row{r}
			break
		}
	}
	for _, a := range state.Asks {
		if a.RowKey == key {
			out.Asks = append(out.Asks, a)
		}
	}
	return out
}

func managerView(state State) State {
	out := state
	out.Rows = make([]Row, len(state.Rows))
	for i, row := range state.Rows {
		row.Configs = nil
		row.Commands = nil
		row.Modes = nil
		row.Preview = liveTranscriptPreview(row.Key, 2)
		out.Rows[i] = row
	}
	return out
}

func publishManagerPreviews(p previewPatch) {
	if controllerConn == nil {
		return
	}
	controllerState.Lock()
	managers := make([]string, 0, len(controllerState.managers))
	for instance := range controllerState.managers {
		managers = append(managers, instance)
	}
	controllerState.Unlock()
	for _, instance := range managers {
		_ = controllerConn.SendAppMsgToBulk(wire.Recipient{InstanceID: instance}, p)
	}
}

func managerSubscriberCount() int {
	controllerState.Lock()
	defer controllerState.Unlock()
	return len(controllerState.managers)
}

func controllerCount() int {
	controllerState.Lock()
	defer controllerState.Unlock()
	return len(controllerState.byKey)
}

// viewSent is the last view each window was sent, as encoded bytes. A
// roster change on one session must not cost every other controller a
// frame: that N-way copy is the fanout this split exists to remove.
//
// viewMu also serializes snapshot-then-send. Two mutators publishing
// concurrently could otherwise each take a snapshot and deliver them in
// the opposite order, leaving a window on the older state until the next
// change happens to arrive.
var (
	viewMu   sync.Mutex
	viewSent = map[string][]byte{}
	viewSend = func(instance string, msg map[string]any) {
		_ = controllerConn.SendAppMsgTo(wire.Recipient{InstanceID: instance}, msg)
	}
)

// sendViewLocked sends msg unless it is byte-identical to what instance
// last received. force sends regardless (a fresh subscribe or claim is
// asking for the current view, and its FE may have remounted).
func sendViewLocked(instance string, msg map[string]any, force bool) {
	b, err := json.Marshal(msg)
	if err == nil && !force && bytes.Equal(viewSent[instance], b) {
		return
	}
	if err == nil {
		viewSent[instance] = b
	}
	viewSend(instance, msg)
}

func sendView(instance string, msg map[string]any) {
	viewMu.Lock()
	defer viewMu.Unlock()
	sendViewLocked(instance, msg, true)
}

func forgetView(instance string) {
	viewMu.Lock()
	delete(viewSent, instance)
	viewMu.Unlock()
}

func publishControllerViews() {
	if svc == nil || controllerConn == nil {
		return
	}
	viewMu.Lock()
	defer viewMu.Unlock()
	snap := svc.Snapshot()
	controllerState.Lock()
	targets := make(map[string]string, len(controllerState.byKey))
	for key, instance := range controllerState.byKey {
		targets[key] = instance
	}
	managers := make([]string, 0, len(controllerState.managers))
	for instance := range controllerState.managers {
		managers = append(managers, instance)
	}
	controllerState.Unlock()
	if len(managers) > 0 {
		msg := map[string]any{"kind": "manager_state", "state": managerView(snap)}
		for _, instance := range managers {
			sendViewLocked(instance, msg, false)
		}
	}
	for key, instance := range targets {
		sendViewLocked(instance, map[string]any{
			"kind": "session_state", "key": key, "state": sessionView(snap, key),
		}, false)
	}
}

func publishControllerUsage(p usagePatch) {
	if controllerConn == nil {
		return
	}
	controllerState.Lock()
	managers := make([]string, 0, len(controllerState.managers))
	for instance := range controllerState.managers {
		managers = append(managers, instance)
	}
	controllerState.Unlock()
	for _, instance := range managers {
		_ = controllerConn.SendAppMsgToBulk(wire.Recipient{InstanceID: instance}, p)
	}
	for _, row := range p.Rows {
		if instance := controllerFor(row.Key); instance != "" {
			_ = controllerConn.SendAppMsgToBulk(wire.Recipient{InstanceID: instance}, usagePatch{
				Kind: p.Kind, Rows: []usagePatchRow{row},
			})
		}
	}
}

func openHosted(conn *sdk.Conn, key string) {
	if lookupHosted(key) == nil {
		return
	}
	if instance := controllerFor(key); instance != "" {
		_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: instance}, map[string]any{"kind": FocusKind, "key": key})
		return
	}
	if !reserveControllerLaunch(key) {
		return
	}
	pendingAttachMu.Lock()
	pendingAttach = append(pendingAttach, key)
	pendingAttachMu.Unlock()
	if err := conn.SpawnRequest(aiAppID); err != nil {
		log.Printf("agentd: open controller key=%s: %v", key, err)
		removePendingAttach(key)
		clearControllerLaunch(key)
		restoreDetached(key)
		return
	}
	log.Printf("agentd: opening controller key=%s", key)
}

func registerControllerHandlers(bus *sdk.Bus) {
	sdk.HandleFromVoid(bus, "manager_subscribe", func(conn *sdk.Conn, _ string, _ struct{}, from wire.Sender) error {
		if from.AppID != "com.wash.agents" || from.InstanceID == "" {
			return nil
		}
		controllerState.Lock()
		controllerState.managers[from.InstanceID] = struct{}{}
		controllerState.Unlock()
		sendView(from.InstanceID, map[string]any{"kind": "manager_state", "state": managerView(svc.Snapshot())})
		return nil
	})
	sdk.HandleFromVoid(bus, "session_claim", func(conn *sdk.Conn, _ string, req transReq, from wire.Sender) error {
		if from.AppID != aiAppID || lookupHosted(req.Key) == nil {
			return nil
		}
		owner, ok := claimController(req.Key, from.InstanceID)
		if !ok {
			_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: owner}, map[string]any{"kind": FocusKind, "key": req.Key})
			return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{"kind": "claim_denied", "key": req.Key})
		}
		hostedMu.Lock()
		h := hostedAll[req.Key]
		if h != nil {
			h.detached = false
		}
		hostedMu.Unlock()
		if h != nil {
			h.republish()
		}
		_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{"kind": "session_claimed", "key": req.Key})
		sendView(from.InstanceID, map[string]any{"kind": "session_state", "key": req.Key, "state": sessionView(svc.Snapshot(), req.Key)})
		return nil
	})
}

func forgetManager(instance string) {
	controllerState.Lock()
	delete(controllerState.managers, instance)
	controllerState.Unlock()
	forgetView(instance)
}

func detachLostController(key string) {
	h := lookupHosted(key)
	if h == nil {
		return
	}
	hostedMu.Lock()
	h.detached = true
	hostedMu.Unlock()
	h.republish()
}
