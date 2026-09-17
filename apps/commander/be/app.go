// Package commander is the Mission Commander service (docs/COMMANDER.md
// §5): the one component that spends anything. On a cadence, while a
// person is attached and the provider is allowed, it observes the eligible
// windows through the router's observe verb, briefs the ones that changed
// through com.wash.inference — batched — and writes each new brief into
// the activity journal, where the Timeline shows it in place.
//
// The router never calls a model; this service never talks to a provider
// itself. Automatic mode is off until switched on, and stays on-box unless
// the hosted switch is ticked too.
package commander

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/inference"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

const AppID = "com.wash.commander"

// Who may change the settings or ask for a tick: the session app (its
// rail toggle goes through its own BE) and Settings. Attested senders
// only — a FE cannot reach this directly.
var settingsCallers = map[string]bool{"com.wash.session": true, "com.wash.settings": true}

var def *sdk.AppDef

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID: AppID, Name: "Mission Commander", Version: version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground, Instancing: sdk.InstancingSingleton,
			Capabilities: []string{sdk.CapObserve, sdk.CapActivityNote},
			// Up with the router, so a person's switch is honoured from the
			// first attach; idle costs a sleeping goroutine while off.
			AutostartAtBoot: true,
			Observation:     sdk.ObservationNone,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{Name: "wash-commander", Manifest: def.Manifest, Run: run})
}

func Def() *sdk.AppDef              { return def }
func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// setReq is a partial update: only the fields present change.
type setReq struct {
	Automatic     *bool `json:"automatic,omitempty"`
	Hosted        *bool `json:"hosted,omitempty"`
	IntervalSec   *int  `json:"interval_sec,omitempty"`
	BudgetPerHour *int  `json:"budget_per_hour,omitempty"`
	BatchMax      *int  `json:"batch_max,omitempty"`
}

var (
	schedMu sync.Mutex
	sched   *scheduler
)

func onReady(c *sdk.Conn, instanceID string, _ uint32) {
	log.Printf("wash-commander ready instance=%s", instanceID)
	settings, err := loadSettings()
	if err != nil {
		log.Printf("wash-commander: config: %v; using defaults", err)
	}
	bus := sdk.NewBus(c)
	client := inference.NewClient(bus)
	states := sdk.NewStateService(bus, State{Settings: settings})

	// Every round trip to the router is bounded: a verb that goes
	// unanswered must cost one tick, never the loop.
	const routerGrace = 10 * time.Second
	s := newScheduler(deps{
		roster: func(ctx context.Context) (sdk.Roster, error) {
			ctx, cancel := context.WithTimeout(ctx, routerGrace)
			defer cancel()
			return c.ObserveRoster(ctx)
		},
		observe: func(ctx context.Context, id string) (wire.Observation, error) {
			ctx, cancel := context.WithTimeout(ctx, routerGrace)
			defer cancel()
			return c.Observe(ctx, id, 0)
		},
		gen: client,
		provider: func(ctx context.Context) (ProviderInfo, error) {
			in, err := client.Info(ctx)
			if err != nil {
				return ProviderInfo{}, err
			}
			return ProviderInfo{Connection: in.Connection, Adapter: in.Adapter, Model: in.Model, Local: in.Local, Available: in.Available, Detail: in.Detail}, nil
		},
		note:    c.Note,
		publish: func(st State) { states.Mutate(func(cur *State) { *cur = st }) },
		logf:    log.Printf,
	}, settings)
	schedMu.Lock()
	sched = s
	schedMu.Unlock()

	sdk.HandleFromVoid(bus, "commander.set", func(_ *sdk.Conn, _ string, req setReq, from wire.Sender) error {
		if !settingsCallers[from.AppID] {
			log.Printf("wash-commander: set refused from=%s", from.AppID)
			return nil
		}
		cur := s.state().Settings
		if req.Automatic != nil {
			cur.Automatic = *req.Automatic
		}
		if req.Hosted != nil {
			cur.Hosted = *req.Hosted
		}
		if req.IntervalSec != nil {
			cur.IntervalSec = *req.IntervalSec
		}
		if req.BudgetPerHour != nil {
			cur.BudgetPerHour = *req.BudgetPerHour
		}
		if req.BatchMax != nil {
			cur.BatchMax = *req.BatchMax
		}
		cur = cur.normalised()
		if err := saveSettings(cur); err != nil {
			log.Printf("wash-commander: save: %v", err)
		}
		log.Printf("wash-commander: set automatic=%v hosted=%v interval=%ds budget=%d/h batch=%d by=%s", cur.Automatic, cur.Hosted, cur.IntervalSec, cur.BudgetPerHour, cur.BatchMax, from.AppID)
		s.setSettings(cur)
		return nil
	})
	// A tick now, dedup and budget still applying — the "Brief now"
	// button, and a test's way of not waiting for the cadence.
	sdk.HandleFromVoid(bus, "commander.run", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) error {
		if !settingsCallers[from.AppID] {
			return nil
		}
		s.kick()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-c.Done(); cancel() }()
	go s.run(ctx)
}
