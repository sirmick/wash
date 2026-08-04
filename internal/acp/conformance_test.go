package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Conformance against a REAL adapter.
//
// types.go is transcribed from the v1 spec, not observed on a wire
// (docs/AGENT_APP.md §12b). This test is how that gets settled, and how it
// stays settled when an adapter updates: it runs the handshake against an
// actual adapter process and asserts that the fields wash depends on are
// populated rather than silently absent — which is what a renamed JSON key
// looks like from Go's side.
//
// Skipped unless told what to run, because it needs an installed adapter
// and (for some) a logged-in CLI. Not a CI gate; a thing you run by hand
// after installing or upgrading an adapter:
//
//	WASH_ACP_ADAPTER='codex-acp' go test ./internal/acp/ -run Conformance -v
//	WASH_ACP_ADAPTER='npx @zed-industries/claude-code-acp' go test ./internal/acp/ -run Conformance -v
//
// WASH_ACP_TRACE=1 additionally dumps every frame in both directions,
// which is the fastest way to diff a real payload against types.go.
func TestConformanceAgainstRealAdapter(t *testing.T) {
	cmdline := os.Getenv("WASH_ACP_ADAPTER")
	if cmdline == "" {
		t.Skip("set WASH_ACP_ADAPTER to run the real-adapter conformance check")
	}
	fields := strings.Fields(cmdline)
	cmd := exec.Command(fields[0], fields[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// An adapter's stderr is where its own diagnostics go; surfacing it is
	// the difference between "the handshake timed out" and "it told you it
	// needs you to log in".
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %q: %v", cmdline, err)
	}
	go func() {
		b, _ := io.ReadAll(stderr)
		if len(b) > 0 {
			t.Logf("adapter stderr:\n%s", b)
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	var r io.Reader = stdout
	var w io.Writer = stdin
	if os.Getenv("WASH_ACP_TRACE") != "" {
		r, w = &tracer{r: stdout, dir: "agent→wash", t: t}, &tracer{w: stdin, dir: "wash→agent", t: t}
	}

	h := &conformanceHandler{t: t}
	c := NewClient(r, w, h)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := c.Initialize(ctx, ClientCapabilities{
		Fs:       FsCapability{ReadTextFile: true, WriteTextFile: true},
		Terminal: true,
	}, Implementation{Name: "wash", Title: "wash", Version: "conformance"})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// The handshake's own fields. An agent that identifies as "" means the
	// key we read is not the key it wrote.
	t.Logf("agent=%+v caps=%+v auth=%v", res.AgentInfo, res.AgentCapabilities, res.AuthMethods)
	if res.AgentInfo.Name == "" {
		t.Error("agentInfo.name empty — InitializeResponse field names may have moved")
	}

	if len(res.AuthMethods) > 0 {
		t.Skipf("adapter requires authentication (%v); log in and re-run", res.AuthMethods)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if sid == "" {
		t.Fatal("session/new returned an empty sessionId — the response field name may have moved")
	}
	t.Logf("session=%s", sid)

	// One trivial turn. What matters is not the answer but that updates
	// arrive and decode into the shapes the roster and transcript read.
	if _, err := c.Prompt(ctx, sid, Text("Reply with the single word: ok")); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}

	seen := h.kinds()
	t.Logf("session/update kinds seen: %v", seen)
	if len(seen) == 0 {
		t.Error("a whole prompt turn produced no session/update — the notification shape has moved")
	}
	if h.undecoded() > 0 {
		t.Errorf("%d updates decoded with an empty sessionUpdate discriminator — check the notification field names", h.undecoded())
	}
}

type conformanceHandler struct {
	t  *testing.T
	mu sync.Mutex
	ks map[string]int
	un int
}

func (h *conformanceHandler) RequestPermission(_ context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error) {
	// Log it in full: this is the single most important payload in the
	// design, and the one v2 restructures.
	b, _ := json.Marshal(req)
	h.t.Logf("session/request_permission: %s", b)
	if req.ToolCall.Kind == "" && req.ToolCall.Title == "" {
		h.t.Error("permission request carried no toolCall fields — v1 shape may not be what this adapter speaks")
	}
	return Cancelled(), nil
}

func (h *conformanceHandler) SessionUpdate(_ context.Context, n SessionNotification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ks == nil {
		h.ks = map[string]int{}
	}
	if n.Update.SessionUpdate == "" {
		h.un++
		return
	}
	h.ks[n.Update.SessionUpdate]++
}

func (h *conformanceHandler) kinds() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for k, v := range h.ks {
		out[k] = v
	}
	return out
}

func (h *conformanceHandler) undecoded() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.un
}

// tracer echoes frames to the test log as they pass.
type tracer struct {
	r   io.Reader
	w   io.Writer
	dir string
	t   *testing.T
}

func (x *tracer) Read(p []byte) (int, error) {
	n, err := x.r.Read(p)
	if n > 0 {
		x.t.Logf("%s %s", x.dir, strings.TrimRight(string(p[:n]), "\n"))
	}
	return n, err
}

func (x *tracer) Write(p []byte) (int, error) {
	x.t.Logf("%s %s", x.dir, strings.TrimRight(string(p), "\n"))
	return x.w.Write(p)
}
