package acp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type titleWatcher struct {
	t      *testing.T
	titles []string
}

func (h *titleWatcher) RequestPermission(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
	return Cancelled(), nil
}
func (h *titleWatcher) SessionUpdate(_ context.Context, n SessionNotification) {
	if n.Update.SessionUpdate == UpdateSessionInfo && n.Update.Title != "" {
		h.titles = append(h.titles, n.Update.Title)
	}
}

// Does an agent RE-title a session as it goes? It matters: agentd names
// the window and the history entry from this, and a name that changes
// every turn is worse than no name at all.
//
// Verified against codex-acp 1.1.9: it titles once, from the first
// prompt, and keeps it across later turns. agentd takes the FIRST title
// regardless, so an adapter that behaves differently cannot churn the
// UI — and this is how we would notice that it does.
func TestTitleAcrossTurns(t *testing.T) {
	cmdline := os.Getenv("WASH_ACP_ADAPTER")
	if cmdline == "" {
		t.Skip("set WASH_ACP_ADAPTER to run the real-adapter title check")
	}
	f := strings.Fields(cmdline)
	cmd := exec.Command(f[0], f[1:]...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	h := &titleWatcher{t: t}
	c := NewClient(stdout, stdin, h)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := c.Initialize(ctx, ClientCapabilities{}, Implementation{Name: "wash"}); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	res, err := c.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Say the word apple.", "Now say the word banana."} {
		if _, err := c.Prompt(ctx, res.SessionID, Text(p)); err != nil {
			t.Fatal(err)
		}
		t.Logf("after %q → titles so far: %q", p, h.titles)
	}
}
