package agenthook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func encode(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := EncodeSettings(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(b)
}

// installed counts wash entries anywhere in a settings object.
func installed(t *testing.T, m map[string]any) int {
	t.Helper()
	n := 0
	hooks, _ := m["hooks"].(map[string]any)
	for _, raw := range hooks {
		list, _ := raw.([]any)
		for _, g := range list {
			group, _ := g.(map[string]any)
			for _, h := range groupHooks(group) {
				if isWashEntry(h) {
					n++
				}
			}
		}
	}
	return n
}

func TestInstallOnEmptySettings(t *testing.T) {
	m := map[string]any{}
	added, updated := Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)
	if added != len(ClaudeHooks) || updated != 0 {
		t.Fatalf("added=%d updated=%d, want %d/0", added, updated, len(ClaudeHooks))
	}
	if got := installed(t, m); got != len(ClaudeHooks) {
		t.Fatalf("%d wash entries, want %d", got, len(ClaudeHooks))
	}
	states, stray := Status(m, ClaudeHooks)
	for _, st := range states {
		if !st.Installed {
			t.Errorf("%s [%s] not reported installed", st.Spec.Event, st.Spec.Matcher)
		}
		if st.Async != st.Spec.Async {
			t.Errorf("%s [%s]: async=%v, want %v", st.Spec.Event, st.Spec.Matcher, st.Async, st.Spec.Async)
		}
		if want := "/usr/bin/wash-agent-hook " + st.Spec.Mode; st.Command != want {
			t.Errorf("command = %q, want %q", st.Command, want)
		}
	}
	if len(stray) != 0 {
		t.Errorf("unexpected stray entries: %v", stray)
	}
}

// Idempotence is the property that lets the panel toggle, the CLI and a
// config-management run all call install without coordinating.
func TestInstallIdempotent(t *testing.T) {
	m := decode(t, `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`)
	if added, updated := Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks); added != len(ClaudeHooks) || updated != 0 {
		t.Fatalf("first install: added=%d updated=%d", added, updated)
	}
	first := encode(t, m)
	for i := 0; i < 3; i++ {
		added, updated := Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)
		if added != 0 || updated != 0 {
			t.Fatalf("re-install %d: added=%d updated=%d, want 0/0", i, added, updated)
		}
		if got := encode(t, m); got != first {
			t.Fatalf("re-install %d changed the file:\n%s", i, got)
		}
	}
}

// Everything the user had must survive: other settings keys, other hooks
// on the same event, and hooks on events we also install into.
func TestInstallIsAdditive(t *testing.T) {
	const src = `{
	  "model": "opus",
	  "permissions": {"allow": ["Bash(git status:*)"]},
	  "hooks": {
	    "Stop": [{"hooks":[{"type":"command","command":"notify-send done"}]}],
	    "PreToolUse": [{"matcher":"Bash","hooks":[{"type":"command","command":"my-audit"}]}],
	    "SessionStart": [{"matcher":"*","hooks":[{"type":"command","command":"my-banner"}]}]
	  }
	}`
	m := decode(t, src)
	Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)

	if got, _ := m["model"].(string); got != "opus" {
		t.Errorf("model lost: %v", m["model"])
	}
	if _, ok := m["permissions"]; !ok {
		t.Error("permissions block lost")
	}
	// The user's own commands are all still there.
	blob := encode(t, m)
	for _, want := range []string{"notify-send done", "my-audit", "my-banner"} {
		if !strings.Contains(blob, want) {
			t.Errorf("user hook %q lost:\n%s", want, blob)
		}
	}
	// Ours joined the existing SessionStart "*" group rather than making
	// a second one.
	hooks := m["hooks"].(map[string]any)
	starts := hooks["SessionStart"].([]any)
	if len(starts) != 1 {
		t.Fatalf("SessionStart has %d groups, want 1", len(starts))
	}
	if n := len(groupHooks(starts[0].(map[string]any))); n != 2 {
		t.Errorf("SessionStart group has %d hooks, want 2 (theirs + ours)", n)
	}
	// PreToolUse: the user's own matcher:"Bash" group is left alone and
	// wash's decide hook joins as a separate matcher:"*" group — the
	// policy callback must see every tool, not just theirs.
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("PreToolUse groups = %d, want 2 (theirs + ours)", len(pre))
	}
	theirs := pre[0].(map[string]any)
	if matcherOf(theirs) != "Bash" || len(groupHooks(theirs)) != 1 {
		t.Errorf("the user's PreToolUse group was modified: %+v", theirs)
	}
	ours := pre[1].(map[string]any)
	if matcherOf(ours) != "*" || !isWashEntry(groupHooks(ours)[0]) {
		t.Errorf("wash's PreToolUse group = %+v", ours)
	}
	if _, hasAsync := groupHooks(ours)[0].(map[string]any)["async"]; hasAsync {
		t.Error("the decide hook must not be async — the agent waits for its answer")
	}
}

// A moved binary (or a hand-edited async flag) is corrected in place, not
// duplicated.
func TestInstallCorrectsExistingEntry(t *testing.T) {
	m := map[string]any{}
	Install(m, "/old/path/wash-agent-hook", ClaudeHooks)
	added, updated := Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)
	if added != 0 {
		t.Errorf("added=%d, want 0 — the entries already existed", added)
	}
	if updated != len(ClaudeHooks) {
		t.Errorf("updated=%d, want %d", updated, len(ClaudeHooks))
	}
	if got := installed(t, m); got != len(ClaudeHooks) {
		t.Errorf("%d entries after re-point, want %d (no duplicates)", got, len(ClaudeHooks))
	}
	if strings.Contains(encode(t, m), "/old/path/") {
		t.Error("old helper path still present")
	}

	// A user who stripped async gets it put back.
	hooks := m["hooks"].(map[string]any)
	stop := hooks["Stop"].([]any)[0].(map[string]any)
	groupHooks(stop)[0].(map[string]any)["async"] = false
	if _, updated := Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks); updated != 1 {
		t.Errorf("updated=%d, want 1 (the async flag)", updated)
	}
}

// Removal is the half that has to be trustworthy: it may only ever take
// out entries wash put in.
func TestRemoveOnlyTouchesMarkedEntries(t *testing.T) {
	const src = `{
	  "model": "opus",
	  "hooks": {
	    "Stop": [{"hooks":[{"type":"command","command":"notify-send done"}]}],
	    "PreToolUse": [{"matcher":"Bash","hooks":[{"type":"command","command":"my-audit"}]}]
	  }
	}`
	before := decode(t, src)
	m := decode(t, src)
	Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)
	if removed := Remove(m); removed != len(ClaudeHooks) {
		t.Fatalf("removed=%d, want %d", removed, len(ClaudeHooks))
	}
	if !reflect.DeepEqual(m, before) {
		t.Errorf("install+remove is not a round trip:\n got %s\nwant %s", encode(t, m), encode(t, before))
	}
}

// Remove prunes what it empties, so an install/remove cycle on a settings
// file that had no hooks at all leaves no debris behind.
func TestRemovePrunesEmptyContainers(t *testing.T) {
	m := decode(t, `{"model":"opus"}`)
	Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)
	Remove(m)
	if _, ok := m["hooks"]; ok {
		t.Errorf("empty hooks object left behind: %s", encode(t, m))
	}
	if got := encode(t, m); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("file not back to its original shape: %s", got)
	}
}

// An older wash that installed a hook the current matrix dropped must
// still be cleanable — and visible in status meanwhile.
func TestRemoveClearsStrayEntriesFromAnOlderMatrix(t *testing.T) {
	m := decode(t, `{"hooks":{"PreCompact":[{"hooks":[{"type":"command","command":"/usr/bin/wash-agent-hook status","async":true}]}]}}`)
	_, stray := Status(m, ClaudeHooks)
	if len(stray) != 1 || stray[0] != "PreCompact" {
		t.Errorf("stray = %v, want [PreCompact]", stray)
	}
	if removed := Remove(m); removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}
	if _, ok := m["hooks"]; ok {
		t.Errorf("hooks not pruned: %s", encode(t, m))
	}
}

// Remove on a file wash never touched is a no-op, including when the
// hooks block holds shapes we don't write.
func TestRemoveNoOpOnForeignShapes(t *testing.T) {
	const src = `{"hooks":{"Stop":"not-a-list","Notification":[{"matcher":"x","hooks":[{"type":"http","url":"https://example"}]},"junk"]}}`
	m := decode(t, src)
	if removed := Remove(m); removed != 0 {
		t.Errorf("removed=%d, want 0", removed)
	}
	if got, want := encode(t, m), encode(t, decode(t, src)); got != want {
		t.Errorf("file changed:\n got %s\nwant %s", got, want)
	}
}

func TestStatusPartial(t *testing.T) {
	m := map[string]any{}
	Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks[:2])
	states, _ := Status(m, ClaudeHooks)
	for i, st := range states {
		want := i < 2
		if st.Installed != want {
			t.Errorf("%s: installed=%v, want %v", st.Spec.Event, st.Installed, want)
		}
	}
}

// A path with a space would break the shell command form; quote it.
func TestCommandQuoting(t *testing.T) {
	if got := commandFor("/opt/my wash/wash-agent-hook", "status"); got != `'/opt/my wash/wash-agent-hook' status` {
		t.Errorf("got %q", got)
	}
	if got := commandFor("/usr/bin/wash-agent-hook", "status"); got != "/usr/bin/wash-agent-hook status" {
		t.Errorf("got %q", got)
	}
	// However it is written, it must still be recognisable as ours.
	if !isWashEntry(newEntry(commandFor("/opt/my wash/wash-agent-hook", "status"), true)) {
		t.Error("quoted entry no longer matches the marker")
	}
}

// Round trip through the filesystem: values we never touch come back
// byte-identical (json.Number), the mode is preserved, and the original
// is backed up once.
func TestLoadSaveSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	const src = `{"bigNumber":12345678901234567890,"float":1.50,"nested":{"keep":true},"model":"opus"}`
	if err := os.WriteFile(path, []byte(src), 0o640); err != nil {
		t.Fatal(err)
	}
	m, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	Install(m, "/usr/bin/wash-agent-hook", ClaudeHooks)
	if err := SaveSettings(path, m); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"12345678901234567890", "1.50", `"keep": true`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("value %q did not survive the round trip:\n%s", want, out)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
	}
	backup, err := os.ReadFile(path + ".wash-bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(backup) != src {
		t.Errorf("backup = %q, want the original file", backup)
	}
	// A second save must not overwrite the backup with the already-
	// modified file.
	if err := SaveSettings(path, m); err != nil {
		t.Fatal(err)
	}
	backup2, _ := os.ReadFile(path + ".wash-bak")
	if string(backup2) != src {
		t.Error("backup was overwritten on the second save")
	}
}

func TestLoadSettingsMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadSettings(filepath.Join(dir, "nope.json"))
	if err != nil || len(m) != 0 {
		t.Errorf("missing file: m=%v err=%v, want empty/nil", m, err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadSettings(empty); err != nil || len(m) != 0 {
		t.Errorf("empty file: m=%v err=%v", m, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(bad); err == nil {
		t.Error("malformed JSON accepted; want an error rather than a clobbered file")
	}
}

// The CLI is the M1 install path, so its verbs are covered end to end
// (both argument orders, since `--path X install` is what muscle memory
// produces).
func TestRunCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if rc := RunCLI([]string{"status", "--path", path}); rc != 1 {
		t.Errorf("status on a fresh file: rc=%d, want 1 (not installed)", rc)
	}
	if rc := RunCLI([]string{"install", "--path", path, "--command", "/usr/bin/wash-agent-hook"}); rc != 0 {
		t.Fatalf("install: rc=%d", rc)
	}
	if rc := RunCLI([]string{"status", "--path", path}); rc != 0 {
		t.Errorf("status after install: rc=%d, want 0", rc)
	}
	// Flags before the verb.
	if rc := RunCLI([]string{"--path", path, "remove"}); rc != 0 {
		t.Fatalf("remove: rc=%d", rc)
	}
	if rc := RunCLI([]string{"status", "--path", path}); rc != 1 {
		t.Errorf("status after remove: rc=%d, want 1", rc)
	}
	if rc := RunCLI([]string{"frobnicate", "--path", path}); rc != 2 {
		t.Errorf("unknown verb: rc=%d, want 2", rc)
	}
	// --dry-run must not create or modify anything.
	fresh := filepath.Join(dir, "fresh.json")
	if rc := RunCLI([]string{"install", "--path", fresh, "--dry-run"}); rc != 0 {
		t.Errorf("dry-run install: rc=%d", rc)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Error("--dry-run wrote the file")
	}
}
