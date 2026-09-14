package agentd

import (
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

func publishControllerViews() {
	if svc == nil || controllerConn == nil {
		return
	}
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
	for _, instance := range managers {
		_ = controllerConn.SendAppMsgTo(wire.Recipient{InstanceID: instance}, map[string]any{
			"kind": "manager_state", "state": managerView(snap),
		})
	}
	for key, instance := range targets {
		_ = controllerConn.SendAppMsgTo(wire.Recipient{InstanceID: instance}, map[string]any{
			"kind": "session_state", "key": key, "state": sessionView(snap, key),
		})
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
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{"kind": "manager_state", "state": managerView(svc.Snapshot())})
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
		_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{"kind": "session_state", "key": req.Key, "state": sessionView(svc.Snapshot(), req.Key)})
		return nil
	})
}

func forgetManager(instance string) {
	controllerState.Lock()
	delete(controllerState.managers, instance)
	controllerState.Unlock()
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
