package agentd

import (
	"os"
	"testing"
)

// Package tests exercise real persistence paths through setState and retire.
// Give every test a disposable default so one that does not need a bespoke
// XDG_STATE_HOME can never overwrite the developer's actual session history.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "wash-agentd-test-state-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
