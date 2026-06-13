// Package txn is the apply transaction engine (docs/NET.md §4.4, §7): it
// validates a staged Config, renders and applies it through an Applier, verifies
// the result, and arms commit-confirm — auto-reverting on a failed verify or a
// missing confirmation. The phase events it emits are the same stream the apply
// terminal renders (§7.1). The engine is pure orchestration; all kernel contact
// is behind the Applier, so it is unit-tested with a fake Applier.
package txn

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/change"
	"github.com/sirmick/wash/internal/washnet/model"
	"github.com/sirmick/wash/internal/washnet/validate"
)

// State is the job's position in the commit-confirm lifecycle.
type State string

const (
	Planning     State = "planning"
	AwaitConfirm State = "await-confirm"
	Committed    State = "committed"
	Reverted     State = "reverted"
	Failed       State = "failed" // refused before any change was applied
)

// Event is one phase-stream entry (§7.1). Seq is monotonic per job.
type Event struct {
	Seq   int
	Phase string // plan|render|apply|verify|await-confirm|committed|reverted
	Level string // info|warn|error
	Msg   string
}

// Job is one apply transaction. After Apply returns it is either AwaitConfirm
// (caller must Confirm or it auto-reverts), Reverted (verify failed), or Failed
// (validation refused it before touching anything).
type Job struct {
	mu      sync.Mutex
	base    model.Config
	target  model.Config
	applier backend.Applier
	token   backend.RollbackToken
	state   State
	diff    change.Diff
	events  []Event
	seq     int
	timer   *time.Timer
}

// Apply runs PLAN→RENDER→APPLY→VERIFY synchronously. On a validation failure it
// returns an error and applies nothing. On a verify failure it auto-reverts and
// returns the Job (no error — inspect State). On success the Job is AwaitConfirm.
func Apply(base, target model.Config, applier backend.Applier) (*Job, error) {
	j := &Job{base: base, target: target, applier: applier, state: Planning}

	j.emit("plan", "info", "validating staged configuration")
	ds := validate.Validate(target, applier.Capabilities())
	if n := errorCount(ds); n > 0 {
		j.emit("plan", "error", fmt.Sprintf("%d validation error(s); nothing applied", n))
		j.state = Failed
		return j, fmt.Errorf("validation failed: %d error(s)", n)
	}

	j.diff = change.Compute(base, target)
	j.emit("render", "info", fmt.Sprintf("rendering %d change(s): %s", len(j.diff.Entries), diffSummary(j.diff)))
	if _, err := applier.Render(target); err != nil {
		j.emit("render", "error", err.Error())
		j.state = Failed
		return j, fmt.Errorf("render: %w", err)
	}

	j.emit("apply", "info", "applying to backend")
	tok, err := applier.Apply(backend.RenderPlan{Target: target, Steps: steps(j.diff)})
	if err != nil {
		j.emit("apply", "error", err.Error())
		j.state = Failed
		return j, fmt.Errorf("apply: %w", err)
	}
	j.token = tok

	j.emit("verify", "info", "verifying applied state")
	if err := applier.Verify(target); err != nil {
		j.emit("verify", "error", "verify failed: "+err.Error())
		if rerr := applier.Rollback(tok); rerr != nil {
			// Failed verify + failed rollback is the worst state the box
			// can be in — never report it as a clean revert.
			j.emit("reverted", "error", "ROLLBACK FAILED after failed verify: "+rerr.Error())
		} else {
			j.emit("reverted", "warn", "auto-reverted after failed verify")
		}
		j.state = Reverted
		return j, nil
	}

	j.emit("await-confirm", "info", "applied; awaiting confirmation")
	j.state = AwaitConfirm
	return j, nil
}

// Confirm commits an AwaitConfirm job, cancelling any auto-revert timer.
func (j *Job) Confirm() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != AwaitConfirm {
		return fmt.Errorf("cannot confirm in state %q", j.state)
	}
	j.stopTimerLocked()
	if err := j.applier.Confirm(j.token); err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	j.emitLocked("committed", "info", "changes confirmed")
	j.state = Committed
	return nil
}

// Revert rolls an AwaitConfirm job back. Called explicitly or by the auto-revert
// timer; idempotent against an already-finished job.
func (j *Job) Revert() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.revertLocked("reverted by request")
}

func (j *Job) revertLocked(reason string) error {
	if j.state != AwaitConfirm {
		return nil // already committed/reverted; nothing to do
	}
	j.stopTimerLocked()
	if err := j.applier.Rollback(j.token); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	j.emitLocked("reverted", "warn", reason)
	j.state = Reverted
	return nil
}

// ArmConfirmTimer starts the autonomous auto-revert: if the job is not confirmed
// within d, it reverts itself (the lock-out protection — washnetd does this
// without the FE). Returns immediately.
func (j *Job) ArmConfirmTimer(d time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != AwaitConfirm {
		return
	}
	j.timer = time.AfterFunc(d, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		_ = j.revertLocked(fmt.Sprintf("auto-reverted: not confirmed within %s", d))
	})
}

func (j *Job) stopTimerLocked() {
	if j.timer != nil {
		j.timer.Stop()
		j.timer = nil
	}
}

// State returns the current lifecycle state.
func (j *Job) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

// Diff returns the staged object-level diff.
func (j *Job) Diff() change.Diff {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.diff
}

// Events returns a copy of the phase stream so far.
func (j *Job) Events() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Event(nil), j.events...)
}

func (j *Job) emit(phase, level, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.emitLocked(phase, level, msg)
}

func (j *Job) emitLocked(phase, level, msg string) {
	j.seq++
	j.events = append(j.events, Event{Seq: j.seq, Phase: phase, Level: level, Msg: msg})
	// Mirror every phase event to the server log: the FE apply terminal
	// shows this stream, but if the apply severs connectivity the FE is
	// gone and the log is the only surviving trail.
	log.Printf("washnet-txn: %s [%s] %s", phase, level, msg)
}

// diffSummary renders the diff entries ("~ network/iface lan, + firewall/zone
// wan") so the trail records WHAT changed, not just how many things did.
// Capped — a huge first-takeover diff shouldn't flood the event stream.
func diffSummary(d change.Diff) string {
	const maxShown = 20
	sym := map[change.Op]string{change.OpAdd: "+", change.OpRemove: "-", change.OpUpdate: "~"}
	parts := make([]string, 0, len(d.Entries))
	for i, e := range d.Entries {
		if i == maxShown {
			parts = append(parts, fmt.Sprintf("… %d more", len(d.Entries)-maxShown))
			break
		}
		parts = append(parts, sym[e.Op]+" "+e.Kind+" "+e.Name)
	}
	return strings.Join(parts, ", ")
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

func steps(d change.Diff) []string {
	out := make([]string, 0, len(d.Entries))
	for _, e := range d.Entries {
		out = append(out, fmt.Sprintf("%s %s %s", e.Op, e.Kind, e.Name))
	}
	return out
}
