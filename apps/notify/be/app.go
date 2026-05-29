// Package notify is wash-notify — the system notification service.
//
// Architecture:
//   - Singleton, surface=background. No window, no FE bundle, no
//     start-menu entry. Autoboot on first shell connect.
//   - Holds the notification history (in-memory, capped) so the
//     sidebar Notification widget can render past events as well as
//     live ones.
//   - Receives notifications via a router-side fan-out: when any app
//     emits EvtNotify, the router both (a) broadcasts ShellNotify to
//     every shell for the transient toast and
//     (b) forwards a cross-app app_msg {kind:"notify", ...} to this
//     service with the originating sender's instance attested.
//
// This v1 has no DND, no per-app mute, no cross-shell read sync; the
// service is the seam where those features land later without touching
// any producer.
//
// Wire shape — inbound from router (kind="notify"):
//
//	{
//	  "kind":  "notify",
//	  "title": "...",
//	  "body":  "...",
//	  "level": "info" | "warn" | "error"
//	}
//
// The router stamps the source via the cross-app `From` field
// (Sender.AppID + Sender.InstanceID); the service never trusts a
// caller-supplied source field.
//
// Wire shape — state pushed to subscribers via sdk.StateService:
//
//	{ "kind":"state", "state": { "notifications":[ {...}, ... ] } }
//
// Subscriber interactions (FE-bound; full set lands with the sidebar
// widget in M5):
//
//	→ subscribe   (sdk.StateService)
//	→ unsubscribe (sdk.StateService)
//	→ mark_read   {id}
//	→ clear_all   {}
package notify

import (
	"context"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

const version = "0.8.0"

// AppID is the reserved app id for the notify service. Exposed so the
// router (and any consumer that needs to address the service
// directly) doesn't have to hardcode the string.
const AppID = "com.wash.notify"

// HistoryCap is the maximum number of notifications the service
// keeps in memory. When exceeded, the oldest is dropped. 100 is more
// than any reasonable session needs to surface; the cap is mostly a
// guardrail against runaway producers blowing up service memory.
const HistoryCap = 100

// State is the publicly visible state shape. The Notifications slice
// is ordered newest-first (most-recent at index 0) so the sidebar
// widget can render top-down without sorting.
type State struct {
	Notifications []Notification `json:"notifications"`
}

// Notification is one entry in the history. ID is a string-stable
// monotonic counter so subscribers can correlate updates across
// snapshots (and so mark_read can address an entry without index
// fragility).
type Notification struct {
	ID         string `json:"id"`
	SourceApp  string `json:"source_app"`
	SourceInst string `json:"source_instance"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Level      string `json:"level"`
	CreatedSec int64  `json:"created_sec"`
	Read       bool   `json:"read"`
}

var def *sdk.AppDef

// nextID is the monotonic id allocator. Atomic so the service can
// later parallelise intake (today we serialise through the bus, but
// the allocator stays correct regardless).
var nextID atomic.Uint64

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Notifications",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-notify",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call. Same
// pattern as every other wash app.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// svc holds the service's state singleton. The OnReady callback fires
// exactly once per process (singleton instancing); we wire it up
// there and keep the reference for the inbound handler closure.
var svc *sdk.StateService[State]

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-notify ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	svc = sdk.NewStateService(bus, State{Notifications: nil})

	// Inbound notification: router → notify service. Cross-app only;
	// `from` is router-attested and identifies the producer. The
	// router's relayNotify branches on AppID so it never delivers a
	// self-notify back here.
	//
	// Three things happen per intake:
	//   1. Append to history (capped, newest-first).
	//   2. Push state to subscribers (StateService.Mutate fans).
	//   3. Re-emit c.Notify so the router's broadcast branch ships a
	//      transient toast to every attached shell. This is the single
	//      authority for toasts — bypassing this path means no toast.
	sdk.HandleFromVoid(bus, "notify", func(conn *sdk.Conn, _ string, req notifyReq, from wire.Sender) error {
		level := req.Level
		switch level {
		case wire.NotifyLevelInfo, wire.NotifyLevelWarn, wire.NotifyLevelError:
		default:
			level = wire.NotifyLevelInfo
		}
		n := Notification{
			ID:         strconv.FormatUint(nextID.Add(1), 10),
			SourceApp:  from.AppID,
			SourceInst: from.InstanceID,
			Title:      req.Title,
			Body:       req.Body,
			Level:      level,
			CreatedSec: time.Now().Unix(),
		}
		svc.Mutate(func(s *State) {
			// Prepend (newest-first ordering). Trim from the tail so
			// the oldest entries fall off when we cross HistoryCap.
			s.Notifications = append([]Notification{n}, s.Notifications...)
			if len(s.Notifications) > HistoryCap {
				s.Notifications = s.Notifications[:HistoryCap]
			}
		})
		log.Printf("wash-notify: %s/%s %s %q", from.AppID, from.InstanceID, level, req.Title)
		// Re-emit so the router broadcasts as ShellNotify. Future DND
		// / filter policy gates this call — for v1 it's unconditional.
		//
		// MUST run on a fresh goroutine: this handler is on the SDK's
		// dispatch path; conn.Notify writes to the same connection.
		// Under burst (or a slow shell), an inline write can fill the
		// router's bounded send queue and block intake of the next
		// inbound notify — head-of-line stalling the queue.
		go func(title, body, lvl string) {
			if err := conn.Notify(title, body, lvl); err != nil {
				log.Printf("wash-notify: re-emit failed: %v", err)
			}
		}(req.Title, req.Body, level)
		return nil
	})

	// FE-bound controls. These are addressed cross-app from the
	// session FE / sidebar widget. We accept own-FE too in case a
	// future debug surface lands in the notify-app's hypothetical FE
	// — both forms flow through HandleFromVoid (own-FE simply has no
	// `from`, which our handler tolerates by treating as
	// system-initiated).
	sdk.HandleFromVoid(bus, "mark_read", func(_ *sdk.Conn, _ string, req markReadReq, _ wire.Sender) error {
		if req.ID == "" {
			return nil
		}
		svc.Mutate(func(s *State) {
			for i := range s.Notifications {
				if s.Notifications[i].ID == req.ID {
					s.Notifications[i].Read = true
					return
				}
			}
		})
		return nil
	})
	sdk.HandleFromVoid(bus, "clear_all", func(_ *sdk.Conn, _ string, _ struct{}, _ wire.Sender) error {
		svc.Mutate(func(s *State) { s.Notifications = nil })
		return nil
	})
}

type notifyReq struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Level string `json:"level"`
}

type markReadReq struct {
	ID string `json:"id"`
}
