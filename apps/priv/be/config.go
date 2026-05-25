package priv

import (
	"os"
	"path/filepath"
	"time"
)

// Config is the runtime tuning wash-priv reads from env. Test code
// builds these directly; main reads them via LoadConfig.
type Config struct {
	// SudoBin is the executable invoked for every privilege
	// escalation. Defaults to "sudo". Test fixtures override with
	// WASH_PRIV_SUDO_BIN pointing at a stub that mimics sudo's
	// -S -k -- interface — see e2e/wash-priv/fakesudo.
	SudoBin string

	// IdleTimeout is the no-request quiet period after which the
	// cached password is zeroed. Set to 0 to disable; we don't
	// recommend that outside of tests.
	IdleTimeout time.Duration

	// AuditPath is the jsonl file every approved/rejected/error
	// request is appended to. Created on first write with mode
	// 0600.
	AuditPath string

	// TermAppID is the wash-term-equivalent app spawned by {kind:"run"}
	// requests with --exec ARGV. Overridable so a stripped-down
	// "wash-term-lite" could be substituted in embedded builds.
	TermAppID string
}

// LoadConfig reads env into a Config. Missing env falls back to
// sensible defaults; never errors.
func LoadConfig() Config {
	c := Config{
		SudoBin:     "sudo",
		IdleTimeout: 15 * time.Minute,
		TermAppID:   "com.wash.term",
	}
	if v := os.Getenv("WASH_PRIV_SUDO_BIN"); v != "" {
		c.SudoBin = v
	}
	if v := os.Getenv("WASH_PRIV_IDLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.IdleTimeout = d
		}
	}
	c.AuditPath = defaultAuditPath()
	if v := os.Getenv("WASH_PRIV_AUDIT_PATH"); v != "" {
		c.AuditPath = v
	}
	if v := os.Getenv("WASH_PRIV_TERM_APP_ID"); v != "" {
		c.TermAppID = v
	}
	return c
}

// defaultAuditPath resolves ~/.local/state/wash/priv-audit.log via
// XDG_STATE_HOME or its fallback. Returns "" if HOME isn't set, in
// which case the audit log is disabled — the alternative is logging
// to a guess, which silently loses the trail.
func defaultAuditPath() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "wash", "priv-audit.log")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "wash", "priv-audit.log")
}
