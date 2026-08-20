// Package hostgw is wash-hostgw — the host awareness gateway
// (docs/SIDEBAR.md M1).
//
// Why it exists: the right rail needs to see B's state, and a shell
// cannot subscribe to a background service directly. Shell-originated
// cross-app sends carry no router-attested `From`, so every service
// rejects them — which is why A's session BE gateways exist at all
// (apps/session/be/app.go). But host B runs --no-session: there is no
// session app there to gateway through.
//
// hostgw is that gateway, expressed as an app instead of as chrome. It
// runs on EVERY router, subscribes to its own host's background
// services as a properly attested app, and republishes each state push
// to its own FE. relayAppMsgToShell fans an app's FE-bound message to
// every attached shell (internal/router/app_session.go), and A's
// tunnelled shell IS an attached shell on B — so B's awareness state
// reaches A over the connection that already exists. No router change,
// no new wire protocol.
//
// Wire shape — inbound from a shell (unattested, see below):
//
//	{ "kind": "subscribe" }
//
// Answered with one republish per service we already hold state for, so
// a late-attaching shell gets the full picture instead of waiting for
// the next change.
//
// Wire shape — inbound from a service (router-attested `From`):
//
//	{ "kind": "state", "state": <opaque> }
//
// Wire shape — republished to our FE (i.e. to every attached shell):
//
//	{ "kind": "hostgw.state", "service": "notify", "state": <verbatim> }
//
// `service` is derived from the router-attested sender app id, never
// from anything the payload claims. State is `any` end to end (mirroring
// apps/session/be's serviceStateMsg): never json.RawMessage or []byte,
// because the router base64-encodes byte strings on the FE-bound hop.
//
// Read-only by design. The subscribe verb is deliberately accepted
// UNATTESTED, because a shell has no attestation to offer and there is
// nothing here to protect: hostgw exposes no writes, holds no secrets of
// its own, and republishes to exactly the audience the router already
// lets attach. Control verbs stay with the session BE gateway and move
// in-app milestone by milestone (SIDEBAR.md M2–M6).
package hostgw

import (
	"context"
	"log"

	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// AppID is the reserved app id for the awareness gateway. The shell
// addresses it by app id ({app_id: …}), which resolveRecipient spawns on
// demand — so the first shell attach brings the singleton up.
const AppID = "com.wash.hostgw"

// FEKind is the envelope kind every republish carries. One kind for all
// services (the service name is a field) so the shell's intercept is a
// single branch and adding a service needs no shell change.
const FEKind = "hostgw.state"

// Reserved app ids for the services we watch. Duplicated as strings
// rather than imported, for the same reason apps/session/be does it: the
// contract is the app-id string, and a compile-time dependency on every
// service package would drag them all into this binary.
const (
	NotifyAppID = "com.wash.notify"
	BulkAppID   = "com.wash.bulk"
	PrivAppID   = "com.wash.priv"
	NetdAppID   = "com.wash.netd"
	AudioAppID  = "com.wash.audio"
	AgentdAppID = "com.wash.agentd"
)

// serviceName maps a service app id to the short name the FE keys its
// (origin, service) awareness map by. Empty string = not ours to speak
// for; a `state` push from anything else is ignored.
//
// The set mirrors serviceFEKind (apps/session/be/app.go) minus
// com.wash.remote: the Hosts widget is already host-aware, and B
// republishing its own host list would say nothing true about B's place
// in A's desktop. Names are the bare service rather than the session
// BE's "<name>.state" kinds — here the kind is fixed and the name is
// data.
func serviceName(appID string) string {
	switch appID {
	case NotifyAppID:
		return "notify"
	case BulkAppID:
		return "bulk"
	case PrivAppID:
		return "priv"
	case NetdAppID:
		return "net"
	case AudioAppID:
		return "audio"
	case AgentdAppID:
		return "agent"
	}
	return ""
}

// watched is the subscribe list, in the order we subscribe at startup.
var watched = []string{
	NotifyAppID, BulkAppID, PrivAppID, NetdAppID, AudioAppID, AgentdAppID,
}

var def *sdk.AppDef

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Host Awareness",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-hostgw",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// cache holds the latest snapshot per service. Package-level, matching
// the other FE-less services' shape: OnReady fires exactly once per
// process under singleton instancing.
var cache = newStateCache()

func onReady(c *sdk.Conn, instanceID string, _ uint32) {
	log.Printf("wash-hostgw ready instance=%s", instanceID)
	bus := sdk.NewBus(c)

	// A service's state push. `from` is router-attested, so the service
	// name is derived from identity, never from the payload — a state
	// message from an app we don't gateway is dropped rather than
	// republished under a guessed name.
	sdk.HandleFromVoid(bus, sdk.StateServiceKindState, func(conn *sdk.Conn, _ string, req serviceStateMsg, from wire.Sender) error {
		name := serviceName(from.AppID)
		if name == "" {
			return nil
		}
		cache.put(name, req.State)
		return republish(conn, name, req.State)
	})

	// A shell asking to be caught up. Unattested by construction: the
	// router relays a shell's cross-app send as a plain EvtAppMsg with no
	// From (internal/router/shell_session.go handleAppMsgSend), so this
	// MUST be HandleVoid — HandleFromVoid returns early when from is nil
	// and would drop every shell subscribe silently. See the package
	// comment for why unattested is safe here and nowhere else.
	//
	// Snapshots are full-replace FE-side, so re-subscribing is idempotent:
	// this is also the staleness answer for a shell that reconnects after
	// a blip (SIDEBAR.md §3.2(4)).
	sdk.HandleVoid(bus, sdk.StateServiceKindSubscribe, func(conn *sdk.Conn, _ string, _ struct{}) error {
		for _, e := range cache.all() {
			if err := republish(conn, e.service, e.state); err != nil {
				return err
			}
		}
		return nil
	})

	// Subscribe to our own host's services. Each send is cross-app from a
	// real app instance, so the router stamps an attested From and the
	// services accept us — the whole reason this is an app.
	//
	// resolveRecipient spawns a background singleton on first reference,
	// so this brings the host's services up on first shell attach. Same
	// cost A's session BE already pays at session start (SIDEBAR.md
	// "Pinned mechanics"), accepted deliberately.
	for _, appID := range watched {
		if err := c.SendAppMsgTo(wire.Recipient{AppID: appID}, map[string]any{
			"kind": sdk.StateServiceKindSubscribe,
		}); err != nil {
			// Best-effort per service: a host with no netd (or a disabled
			// service) must not cost us the others.
			log.Printf("wash-hostgw: subscribe %s: %v", appID, err)
		}
	}
}

// republish ships one service's snapshot to our own FE — which the
// router fans to every attached shell, local and tunnelled alike.
func republish(c *sdk.Conn, service string, state any) error {
	return c.SendAppMsg(map[string]any{
		"kind":    FEKind,
		"service": service,
		"state":   state,
	})
}

// serviceStateMsg captures the `state` field of a StateService push. The
// body is opaque — each service has its own shape and we forward
// verbatim. `any`, never json.RawMessage/[]byte: the router
// base64-encodes byte strings on the FE-bound hop.
type serviceStateMsg struct {
	State any `json:"state"`
}
