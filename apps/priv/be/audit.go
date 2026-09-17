package priv

import (
	"encoding/json"
	"github.com/sirmick/wash/pkg/wire"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// auditRecord is one row in the jsonl audit log. The fields are
// deliberately flat for grep-ability; we never nest. Decision is the
// canonical "what happened" verb: approve | reject | error |
// password_failed | idle_locked | refresh_locked | locked.
type auditRecord struct {
	TS          string   `json:"ts"`
	Decision    string   `json:"decision"`
	ReqID       string   `json:"req_id,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	SenderApp   string   `json:"sender_app,omitempty"`
	SenderInst  string   `json:"sender_inst,omitempty"`
	AppID       string   `json:"app_id,omitempty"`
	Argv        []string `json:"argv,omitempty"`
	SpawnedInst string   `json:"spawned_inst,omitempty"`
	ExitCode    int      `json:"exit_code,omitempty"`
	Error       string   `json:"error,omitempty"`
	DurationMs  int64    `json:"duration_ms,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	// Router-attested wash-sudo caller identity, when the sender is a
	// cli-* session. Without these the audit row says which app asked
	// but not which process/tty drove it.
	CliPID int64 `json:"cli_pid,omitempty"`
	// Pointer so a root caller (uid 0) still serializes — omitempty
	// would silently drop the one uid an audit reader cares most about.
	CliUID  *int64 `json:"cli_uid,omitempty"`
	CliComm string `json:"cli_comm,omitempty"`
	CliTTY  string `json:"cli_tty,omitempty"`
}

// auditRecordFromRequest builds an audit row from a request and the
// decision string. Decision separates approve / reject / error
// because the request lifecycle alone doesn't distinguish them once
// they reach a terminal status.
func auditRecordFromRequest(r *Request, decision string) auditRecord {
	rec := auditRecord{
		TS:          time.Now().UTC().Format(time.RFC3339),
		Decision:    decision,
		ReqID:       r.ReqID,
		Kind:        string(r.Kind),
		SenderApp:   r.Sender.AppID,
		SenderInst:  r.Sender.InstanceID,
		AppID:       r.AppID,
		Argv:        r.Argv,
		SpawnedInst: r.SpawnedInst,
		ExitCode:    r.ExitCode,
		Error:       r.ErrorMsg,
		Reason:      stripControl(r.Reason),
	}
	if !r.StartedAt.IsZero() && !r.FinishedAt.IsZero() {
		rec.DurationMs = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
	}
	if o := r.CliOrigin; o != nil {
		rec.CliPID = o.PID
		uid := o.UID
		rec.CliUID = &uid
		rec.CliComm = o.Comm
		rec.CliTTY = o.TTY
	}
	return rec
}

// appendAudit writes one JSON line to the configured audit path.
// Silent on missing path (cfg.AuditPath == ""); logs other I/O
// failures to stderr because losing audit-log lines is bad but
// shouldn't crash the privilege primitive.
//
// Single writer (we hold s.mu when called), so no inter-process
// locking needed. If/when wash-priv ever sheds its singleton-ness,
// switch to O_APPEND + flock.
func (s *State) appendAudit(rec auditRecord) {
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}
	s.journal(rec)
	if cfg.AuditPath == "" {
		return
	}
	if err := ensureDir(cfg.AuditPath); err != nil {
		log.Printf("wash-priv: audit ensureDir: %v", err)
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		log.Printf("wash-priv: audit marshal: %v", err)
		return
	}
	f, err := os.OpenFile(cfg.AuditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("wash-priv: audit open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		log.Printf("wash-priv: audit write: %v", err)
	}
}

// ensureDir creates the parent dir of path with mode 0700 if it
// doesn't already exist. Idempotent.
func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "/" {
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

// journal mirrors a decision into the router's activity journal
// (docs/COMMANDER.md §3.2): who asked for privilege, for what, and what
// the person decided — the one line, beside the audit file that keeps the
// whole record. Lock-state housekeeping (idle_locked) is not a decision
// and is not journaled.
func (s *State) journal(rec auditRecord) {
	if s.conn == nil {
		return
	}
	switch rec.Decision {
	case "approve", "reject", "error", "approve_app", "revoke_app":
	default:
		return
	}
	line := rec.Decision
	if rec.SenderApp != "" {
		line += " " + rec.SenderApp
	}
	if len(rec.Argv) > 0 {
		line += ": " + strings.Join(rec.Argv, " ")
	}
	if rec.Error != "" {
		line += " (" + rec.Error + ")"
	}
	_ = s.conn.Note(wire.EvtActivityNote{
		Kind: "priv.escalate", Title: rec.SenderApp, Line: line,
		Ref: map[string]any{"req_id": rec.ReqID, "decision": rec.Decision},
	})
}
