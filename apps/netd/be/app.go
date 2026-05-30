// Package netd is wash-netd (com.wash.netd) — the privileged networking
// service (docs/NET.md §2.11, §3). It is a wash *background singleton service*
// (no window, no FE bundle, reserved id), modeled on apps/priv/be: it links the
// pure internal/washnet library + an Applier backend, receives requests by
// cross-app app_msg from the windowed com.wash.net app (router-attested sender),
// and publishes its status to the sidebar via sdk.StateService.
//
// Privilege boundary (§3): only this service is privileged. It authorizes
// mutating requests by From.AppID == com.wash.net; the reserved-id registry gate
// (internal/router/registry.go) refuses any untrusted binary claiming this id.
//
// Cross-app wire (from com.wash.net; each carries req_id for reply correlation):
//
//	→ {kind:"validate", config:{…}}         ← {kind:"validate_ok", diagnostics:[…]}
//	→ {kind:"diff",     config:{…}}          ← {kind:"diff_ok", entries:[…], summary:[…]}
//	→ {kind:"apply",    config:{…}}          ← {kind:"apply_ok", state, events:[…], entries:[…]}
//	→ {kind:"confirm"}                       ← {kind:"confirm_ok", state}
//	→ {kind:"revert"}                        ← {kind:"revert_ok", state}
//
// `config` is the FE interchange JSON (codec.DecodeJSON, §2.11). State pushed to
// subscribers via sdk.StateService: {kind:"state", state:{status, phase, summary,
// diagnostics}}.
//
// B1a wires this against a fake/echo Applier (fake_applier.go); the real NM
// backend is B4. The autonomous commit-confirm timer (§7) is armed in B2 — here
// confirm/revert are explicit, so the message-injection tests stay deterministic.
package netd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
	"github.com/sirmick/wash/internal/washnet/change"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/txn"
	"github.com/sirmick/wash/internal/washnet/validate"
)

const version = "0.8.0"

// AppID is the reserved id this service claims. The registry refuses any
// non-trusted binary from serving it (internal/router/registry.go reservedIDs),
// so a malicious local app cannot shadow netd to inherit its privilege.
const AppID = "com.wash.netd"

// NetAppID is the windowed app allowed to drive mutating requests. The router
// attests the sender, so this is a real authorization boundary, not a hint.
const NetAppID = "com.wash.net"

// NetState is the status snapshot published to the sidebar (via the session-BE
// gateway, B1d). Minimal for B1a; the apply terminal stream is B3.
type NetState struct {
	Status      string                `json:"status"` // idle|await-confirm|committed|reverted|failed
	Phase       string                `json:"phase,omitempty"`
	Summary     []string              `json:"summary,omitempty"`
	Diagnostics []validate.Diagnostic `json:"diagnostics,omitempty"`
}

var def *sdk.AppDef

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Network",
			Version:         version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-netd",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// Service singletons. OnReady fires exactly once (singleton instancing); the
// handler closures capture these. A package-level mutex guards the pending job.
var (
	svc     *sdk.StateService[NetState]
	applier *fakeApplier
	mu      sync.Mutex
	pending *txn.Job // set while a change awaits confirm; nil otherwise
)

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-netd ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	applier = newFakeApplier()
	svc = sdk.NewStateService(bus, NetState{Status: "idle"})
	registerHandlers(bus)
}

func registerHandlers(bus *sdk.Bus) {
	sdk.HandleFrom(bus, "validate", func(_ *sdk.Conn, _ string, req configReq, from wire.Sender) (validateResp, error) {
		if err := authz(from); err != nil {
			return validateResp{}, err
		}
		cfg, err := decodeConfig(req.Config)
		if err != nil {
			return validateResp{}, sdk.Errf(sdk.ErrBadRequest, "decode config: %v", err)
		}
		return validateResp{Diagnostics: nonNilDiags(validate.Validate(cfg, applier.Capabilities()))}, nil
	})

	sdk.HandleFrom(bus, "diff", func(_ *sdk.Conn, _ string, req configReq, from wire.Sender) (diffResp, error) {
		if err := authz(from); err != nil {
			return diffResp{}, err
		}
		cfg, err := decodeConfig(req.Config)
		if err != nil {
			return diffResp{}, sdk.Errf(sdk.ErrBadRequest, "decode config: %v", err)
		}
		d := change.Compute(applier.Live(), cfg)
		return diffResp{Entries: entryDTOs(d), Summary: summarize(d)}, nil
	})

	sdk.HandleFrom(bus, "apply", func(_ *sdk.Conn, _ string, req configReq, from wire.Sender) (applyResp, error) {
		if err := authz(from); err != nil {
			return applyResp{}, err
		}
		cfg, err := decodeConfig(req.Config)
		if err != nil {
			return applyResp{}, sdk.Errf(sdk.ErrBadRequest, "decode config: %v", err)
		}
		// Surface diagnostics to the FE on a refused apply (the txn engine
		// re-validates internally but doesn't return the findings).
		if ds := validate.Validate(cfg, applier.Capabilities()); errorCount(ds) > 0 {
			publish(NetState{Status: "failed", Diagnostics: nonNilDiags(ds)})
			return applyResp{State: string(txn.Failed), Diagnostics: nonNilDiags(ds)}, nil
		}
		mu.Lock()
		if pending != nil {
			mu.Unlock()
			return applyResp{}, sdk.Errf(sdk.ErrBadRequest, "a change is already awaiting confirmation")
		}
		mu.Unlock()

		job, err := txn.Apply(applier.Live(), cfg, applier)
		if err != nil {
			return applyResp{State: string(job.State()), Events: eventDTOs(job)}, sdk.Errf(sdk.ErrInternal, "apply: %v", err)
		}
		resp := applyResp{State: string(job.State()), Events: eventDTOs(job), Entries: entryDTOs(job.Diff())}
		switch job.State() {
		case txn.AwaitConfirm:
			mu.Lock()
			pending = job
			mu.Unlock()
			publish(NetState{Status: string(txn.AwaitConfirm), Phase: lastPhase(job), Summary: summarize(job.Diff())})
		case txn.Reverted:
			publish(NetState{Status: string(txn.Reverted), Phase: lastPhase(job)})
		}
		return resp, nil
	})

	sdk.HandleFrom(bus, "confirm", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (statusResp, error) {
		if err := authz(from); err != nil {
			return statusResp{}, err
		}
		mu.Lock()
		job := pending
		pending = nil
		mu.Unlock()
		if job == nil {
			return statusResp{}, sdk.Errf(sdk.ErrBadRequest, "no change awaiting confirmation")
		}
		if err := job.Confirm(); err != nil {
			mu.Lock()
			pending = job // confirm failed; the job is still awaiting, restore it
			mu.Unlock()
			return statusResp{}, sdk.Errf(sdk.ErrInternal, "confirm: %v", err)
		}
		publish(NetState{Status: string(txn.Committed)})
		return statusResp{State: string(job.State())}, nil
	})

	sdk.HandleFrom(bus, "revert", func(_ *sdk.Conn, _ string, _ struct{}, from wire.Sender) (statusResp, error) {
		if err := authz(from); err != nil {
			return statusResp{}, err
		}
		mu.Lock()
		job := pending
		pending = nil
		mu.Unlock()
		if job == nil {
			return statusResp{}, sdk.Errf(sdk.ErrBadRequest, "no change awaiting confirmation")
		}
		if err := job.Revert(); err != nil {
			return statusResp{}, sdk.Errf(sdk.ErrInternal, "revert: %v", err)
		}
		publish(NetState{Status: string(txn.Reverted)})
		return statusResp{State: string(job.State())}, nil
	})
}

// authz enforces the privilege boundary: only the windowed com.wash.net app may
// drive mutating requests. Subscribe/unsubscribe (StateService) are open.
func authz(from wire.Sender) error {
	if from.AppID != NetAppID {
		return sdk.Errf(sdk.ErrForbidden, "only %s may drive netd (got %q)", NetAppID, from.AppID)
	}
	return nil
}

func publish(s NetState) {
	if svc != nil {
		svc.Mutate(func(cur *NetState) { *cur = s })
	}
}

// --- wire request/response types -------------------------------------------

// configReq carries the FE interchange JSON as a decoded map (NOT json.RawMessage:
// the router base64-encodes byte strings, cf. the CBOR/JSON pitfall). decodeConfig
// re-marshals it for codec.DecodeJSON.
type configReq struct {
	Config map[string]any `json:"config"`
}

type validateResp struct {
	Diagnostics []validate.Diagnostic `json:"diagnostics"`
}

type diffResp struct {
	Entries []entryDTO `json:"entries"`
	Summary []string   `json:"summary"`
}

type applyResp struct {
	State       string                `json:"state"`
	Events      []eventDTO            `json:"events"`
	Entries     []entryDTO            `json:"entries,omitempty"`
	Diagnostics []validate.Diagnostic `json:"diagnostics,omitempty"`
}

type statusResp struct {
	State string `json:"state"`
}

// entryDTO / eventDTO are snake_case wire shapes for the pure domain types
// (change.Entry / txn.Event have no json tags — kept pure).
type entryDTO struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Op   string `json:"op"`
}

type eventDTO struct {
	Seq   int    `json:"seq"`
	Phase string `json:"phase"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// --- helpers ----------------------------------------------------------------

func decodeConfig(m map[string]any) (model.Config, error) {
	if m == nil {
		return model.Config{}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return model.Config{}, err
	}
	return codec.DecodeJSON(b)
}

func entryDTOs(d change.Diff) []entryDTO {
	out := make([]entryDTO, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, entryDTO{Kind: e.Kind, Name: e.Name, Op: string(e.Op)})
	}
	return out
}

func eventDTOs(j *txn.Job) []eventDTO {
	evs := j.Events()
	out := make([]eventDTO, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventDTO{Seq: e.Seq, Phase: e.Phase, Level: e.Level, Msg: e.Msg})
	}
	return out
}

func summarize(d change.Diff) []string {
	out := make([]string, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, fmt.Sprintf("%s %s %s", e.Op, e.Kind, e.Name))
	}
	return out
}

func lastPhase(j *txn.Job) string {
	evs := j.Events()
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Phase
}

func errorCount(ds []validate.Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == validate.Error {
			n++
		}
	}
	return n
}

// nonNilDiags ensures the JSON is [] not null, so the FE always gets an array.
func nonNilDiags(ds []validate.Diagnostic) []validate.Diagnostic {
	if ds == nil {
		return []validate.Diagnostic{}
	}
	return ds
}
