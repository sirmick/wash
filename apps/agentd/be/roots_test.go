package agentd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
)

// The cwd is the default scope, not the limit: a change in one package of
// a monorepo routinely needs to read a sibling, and the only alternative
// was starting the agent at a parent — granting far more than the two
// folders it needed.
func TestExtraRootMakesASiblingReadable(t *testing.T) {
	withStateDir(t)
	withState(t, 0)
	base := t.TempDir()
	work := filepath.Join(base, "app")
	sib := filepath.Join(base, "lib")
	for _, d := range []string{work, sib} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(sib, "schema.json")
	if err := os.WriteFile(shared, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &hosted{key: "acp:roots", cwd: work}

	if _, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: shared}); err == nil {
		t.Fatal("the sibling was readable before it was allowed")
	}
	if !h.addRoot(sib) {
		t.Fatal("addRoot refused a plain sibling directory")
	}
	res, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: shared})
	if err != nil || res.Content != "{}\n" {
		t.Fatalf("read after allowing: %q, %v", res.Content, err)
	}
	// And a write, because a root an agent may read but not write is a
	// distinction nobody asked for.
	if err := h.WriteTextFile(context.Background(), acp.WriteTextFileRequest{Path: shared, Content: "{\"a\":1}\n"}); err != nil {
		t.Fatalf("write after allowing: %v", err)
	}
	if b, _ := os.ReadFile(shared); string(b) != "{\"a\":1}\n" {
		t.Errorf("on disk: %q", b)
	}

	// Taking it back closes it again.
	if !h.removeRoot(sib) {
		t.Fatal("removeRoot did not find the root it had just added")
	}
	if _, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: shared}); err == nil {
		t.Error("the sibling stayed readable after the root was removed")
	}
}

// The terminal honours the SAME set, because a folder an agent may read
// and may not run anything in is a distinction nobody would maintain.
func TestExtraRootIsHonouredByTheTerminalConfinement(t *testing.T) {
	withStateDir(t)
	withState(t, 0)
	base := t.TempDir()
	work, sib := filepath.Join(base, "app"), filepath.Join(base, "lib")
	for _, d := range []string{work, sib} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := &hosted{key: "acp:roots2", cwd: work}

	if _, err := h.confine(sib); err == nil {
		t.Fatal("the sibling resolved before it was allowed")
	}
	h.addRoot(sib)
	got, err := h.confine(sib)
	if err != nil || got != sib {
		t.Errorf("confine = %q, %v", got, err)
	}
	// CreateTerminal resolves its cwd through the very same call, so the
	// terminal and the fs cannot drift apart (the pty itself needs a live
	// conn, which is not what this is about).
	got, err = h.confineOrAsk(context.Background(), "Bash", sib)
	if err != nil || got != sib {
		t.Errorf("terminal cwd = %q, %v — the terminal does not share the fs roots", got, err)
	}
	// A path outside every root is still refused with nobody to ask.
	if _, err := h.confineOrAsk(context.Background(), "Bash", t.TempDir()); err == nil {
		t.Error("a cwd outside every root was accepted")
	}
}

func TestAddRootRefusesRootDirectoryDuplicatesAndOverflow(t *testing.T) {
	base := t.TempDir()
	h := &hosted{key: "acp:roots3", cwd: filepath.Join(base, "work")}

	// "/" is not an extra root, it is the absence of confinement.
	if h.addRoot("/") {
		t.Error("/ was accepted as an extra root")
	}
	// Already covered by the cwd, or by a root already added.
	if h.addRoot(filepath.Join(base, "work", "sub")) {
		t.Error("a path inside the cwd was added as a root")
	}
	d := t.TempDir()
	if !h.addRoot(d) {
		t.Fatal("a fresh directory was refused")
	}
	if h.addRoot(d) {
		t.Error("the same root was added twice")
	}
	if h.addRoot(filepath.Join(d, "inner")) {
		t.Error("a path inside an existing root was added again")
	}
	for i := 0; i < maxExtraRoots+2; i++ {
		h.addRoot(t.TempDir())
	}
	if got := len(h.extraRootsSnapshot()); got != maxExtraRoots {
		t.Errorf("roots = %d, want the cap of %d", got, maxExtraRoots)
	}
	// The published set is a copy: a later append must not rewrite a row
	// somebody already holds.
	snap := h.extraRootsSnapshot()
	h.removeRoot(snap[0])
	if len(snap) != maxExtraRoots {
		t.Error("extraRootsSnapshot aliased the live slice")
	}
}

// A path outside every root asks rather than failing silently. With a
// desktop attached and an "allow", the read goes through — once, without
// widening the session, because widening is a separate deliberate act
// whose result is visible in the status bar.
func TestOutsideEveryRootAsksAndAnAllowLetsItThroughOnce(t *testing.T) {
	withStateDir(t)
	withState(t, 1)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(outside, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetAsks()
	h := &hosted{key: "acp:roots4", cwd: root}
	h.register()
	t.Cleanup(reset)

	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := h.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: outside})
		if err != nil || res.Content != "hello\n" {
			t.Errorf("read after allowing: %q, %v", res.Content, err)
		}
	}()

	// Answer it the way the desktop does: by id, through the queue.
	p := waitForAsk(t, h.key)
	mutateState(func(st *State) { delete(asks, p.ID) })
	if err := p.reply(DecisionAllow, ReasonDesktop); err != nil {
		t.Fatal(err)
	}
	<-done

	// Allowed once: the session is no wider than it was.
	if got := h.extraRootsSnapshot(); got != nil {
		t.Errorf("an approval widened the session: %v", got)
	}
	if want := []string{root}; !reflect.DeepEqual(h.roots(), want) {
		t.Errorf("roots = %v, want %v", h.roots(), want)
	}
}

// waitForAsk spins until the question the read raised is in the queue.
func waitForAsk(t *testing.T, rowKey string) *pending {
	t.Helper()
	for i := 0; i < 200; i++ {
		var found *pending
		mutateState(func(*State) {
			for _, p := range asks {
				if p.RowKey == rowKey {
					found = p
				}
			}
		})
		if found != nil {
			return found
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no question was raised for a path outside every root")
	return nil
}
