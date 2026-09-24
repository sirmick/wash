package agentd

import (
	"reflect"
	"sync"
	"testing"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// watchDesktop records desktop events instead of acting on them.
func watchDesktop(t *testing.T) func() []any {
	t.Helper()
	var mu sync.Mutex
	var got []any
	old := desktop
	desktop = func(_ *sdk.Conn, ev any) { mu.Lock(); got = append(got, ev); mu.Unlock() }
	t.Cleanup(func() { desktop = old })
	return func() []any { mu.Lock(); defer mu.Unlock(); return append([]any(nil), got...) }
}

// Opening a session's window and telling the person about a question are
// desktop events, not calls scattered through agentd: the one handler is
// what a desktop other than Wash's would replace.
func TestSideEffectsAreDesktopEvents(t *testing.T) {
	events := watchDesktop(t)
	resetControllersForTest()
	t.Cleanup(resetControllersForTest)
	hostedMu.Lock()
	hostedAll["acp:1"] = &hosted{key: "acp:1"}
	hostedMu.Unlock()
	t.Cleanup(func() { hostedMu.Lock(); delete(hostedAll, "acp:1"); hostedMu.Unlock() })

	openHosted(nil, "acp:1")
	// A second open while the first window is still starting opens nothing.
	openHosted(nil, "acp:1")
	installAskToasts(nil)
	notifyAsk(agentproto.Ask{RowKey: "acp:1", Agent: "claude", Tool: "Bash", Subject: "rm -rf /"})

	want := []any{
		agentproto.OpenSession{Key: "acp:1"},
		agentproto.Notify{Key: "acp:1", Title: "claude needs you", Body: "Bash rm -rf /", Level: wire.NotifyLevelWarn},
	}
	if got := events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop events = %#v, want %#v", got, want)
	}
}
