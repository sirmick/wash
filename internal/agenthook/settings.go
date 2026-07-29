// Hook installation: an additive, idempotent, reversible merge into the
// agent's settings file (docs/AGENT_TERM.md §4 "Install").
//
// Three properties the merge is built to guarantee, because it edits a
// file the user owns and may have hand-written:
//
//   - Additive: existing hooks, and every other key in the file, are
//     carried through untouched. We only ever append our own entries.
//   - Idempotent: installing twice changes nothing the second time.
//   - Marked: our entries are identified by the helper name inside the
//     command string, so removal can only ever take out entries wash
//     put there. Nothing else in the file is a candidate.
//
// The marker is the command itself rather than a custom JSON key on
// purpose: agent settings schemas validate hook entries, and an unknown
// field is a good way to get the whole block rejected.
package agenthook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HookMarker identifies a hook entry as wash's. Any command string
// containing it is ours; nothing else is ever touched.
const HookMarker = "wash-agent-hook"

// HookSpec is one row of the install matrix.
type HookSpec struct {
	// Event is the agent's hook event name ("SessionStart", …).
	Event string
	// Matcher narrows the event. Empty means the entry carries no
	// matcher key at all (the event has nothing to match on).
	Matcher string
	// Mode is the helper mode the entry runs ("status" | "decide").
	Mode string
	// Async runs the hook in the background so it can never block a turn.
	// True for every status hook; false for decide, which is inherently
	// synchronous — the agent is waiting for the answer.
	Async bool
}

// ClaudeHooks is the Claude Code matrix from docs/AGENT_TERM.md §4,
// verified against Claude Code 2.1's hook schema: Notification's matcher
// selects on notification_type, and every status hook is async so it can
// never block a turn.
var ClaudeHooks = []HookSpec{
	{Event: "SessionStart", Matcher: "*", Mode: "status", Async: true},
	{Event: "UserPromptSubmit", Mode: "status", Async: true},
	{Event: "Notification", Matcher: "permission_prompt", Mode: "status", Async: true},
	{Event: "Notification", Matcher: "idle_prompt", Mode: "status", Async: true},
	{Event: "Stop", Mode: "status", Async: true},
	{Event: "SessionEnd", Mode: "status", Async: true},
	// The policy callback (§6). Installed unconditionally, and inert
	// until a policy exists: with no rules the helper prints nothing and
	// the agent's own prompt appears exactly as before. Sync by nature —
	// the agent is blocked on the answer — so it is NOT async, and the
	// helper's own 3s deadline is what bounds it.
	{Event: "PreToolUse", Matcher: "*", Mode: "decide"},
}

// HookState is one matrix row's installed state, for `agent-hooks status`
// and (later) the Settings → Agents panel.
type HookState struct {
	Spec HookSpec
	// Installed is true when a wash entry exists for this row.
	Installed bool
	// Command is the command string found, when installed.
	Command string
	// Async is the entry's async flag; false is a misconfiguration (a
	// synchronous status hook would stall the agent's turn).
	Async bool
}

// Install merges the matrix into settings, which is a decoded settings
// file (use LoadSettings). command is the full command string to run,
// minus the mode — "/usr/bin/wash-agent-hook".
//
// Returns how many entries were added and how many existing wash entries
// were corrected (a moved binary, a lost async flag). Both zero means the
// file already said exactly the right thing.
func Install(settings map[string]any, command string, matrix []HookSpec) (added, updated int) {
	hooks := ensureMap(settings, "hooks")
	for _, spec := range matrix {
		want := commandFor(command, spec.Mode)
		list, _ := hooks[spec.Event].([]any)
		group, entry := findEntry(list, spec.Matcher)
		switch {
		case entry != nil:
			// Already ours — reconcile the fields we own.
			if s, _ := entry["command"].(string); s != want {
				entry["command"] = want
				updated++
			}
			if b, _ := entry["async"].(bool); b != spec.Async {
				if spec.Async {
					entry["async"] = true
				} else {
					delete(entry, "async")
				}
				updated++
			}
		case group != nil:
			// Someone else already hooks this event+matcher; add ours
			// alongside rather than making a second group.
			group["hooks"] = append(groupHooks(group), newEntry(want, spec.Async))
			added++
		default:
			g := map[string]any{"hooks": []any{newEntry(want, spec.Async)}}
			if spec.Matcher != "" {
				g["matcher"] = spec.Matcher
			}
			hooks[spec.Event] = append(list, g)
			added++
		}
	}
	return added, updated
}

// Remove deletes every wash-marked hook entry from settings — including
// entries from an older matrix that the current one no longer installs —
// and prunes whatever it empties (a group with no hooks, an event with no
// groups, the hooks object itself). Nothing unmarked is touched.
//
// Returns the number of entries removed.
func Remove(settings map[string]any) (removed int) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}
	for event, raw := range hooks {
		list, ok := raw.([]any)
		if !ok {
			continue // not a shape we wrote; leave it alone
		}
		keptGroups := make([]any, 0, len(list))
		for _, g := range list {
			group, ok := g.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, g)
				continue
			}
			kept := make([]any, 0, len(groupHooks(group)))
			for _, h := range groupHooks(group) {
				if isWashEntry(h) {
					removed++
					continue
				}
				kept = append(kept, h)
			}
			if len(kept) == 0 {
				// The group existed only for us (or was already empty).
				continue
			}
			group["hooks"] = kept
			keptGroups = append(keptGroups, group)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = keptGroups
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return removed
}

// Status reports the matrix row by row, plus any wash entries found under
// events the matrix doesn't cover (leftovers from an older install).
func Status(settings map[string]any, matrix []HookSpec) ([]HookState, []string) {
	hooks, _ := settings["hooks"].(map[string]any)
	out := make([]HookState, 0, len(matrix))
	covered := map[string]bool{}
	for _, spec := range matrix {
		covered[spec.Event] = true
		st := HookState{Spec: spec}
		if hooks != nil {
			list, _ := hooks[spec.Event].([]any)
			if _, entry := findEntry(list, spec.Matcher); entry != nil {
				st.Installed = true
				st.Command, _ = entry["command"].(string)
				st.Async, _ = entry["async"].(bool)
			}
		}
		out = append(out, st)
	}
	var stray []string
	for event, raw := range hooks {
		if covered[event] {
			continue
		}
		list, _ := raw.([]any)
		for _, g := range list {
			group, ok := g.(map[string]any)
			if !ok {
				continue
			}
			for _, h := range groupHooks(group) {
				if isWashEntry(h) {
					stray = append(stray, event)
				}
			}
		}
	}
	return out, stray
}

// ---- entry helpers ----

func newEntry(command string, async bool) map[string]any {
	// async: a status helper writes one short sequence to a tty and
	// exits, so it must never sit in the agent's critical path. The
	// decide helper is the exception — the agent is waiting for its
	// answer, so it cannot be backgrounded.
	e := map[string]any{"type": "command", "command": command}
	if async {
		e["async"] = true
	}
	return e
}

// commandFor renders the command string for a mode, quoting a path that
// contains shell-significant characters (the entry runs through a shell
// unless the agent supports the exec form, which older versions don't).
func commandFor(command, mode string) string {
	if strings.ContainsAny(command, " \t'\"$`\\") {
		command = "'" + strings.ReplaceAll(command, "'", `'\''`) + "'"
	}
	if mode == "" {
		return command
	}
	return command + " " + mode
}

// findEntry locates the group for a matcher within one event's list, and
// wash's entry inside it. Either may be nil: no group, or a group that
// exists but holds only other people's hooks.
func findEntry(list []any, matcher string) (map[string]any, map[string]any) {
	for _, g := range list {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if matcherOf(group) != matcher {
			continue
		}
		for _, h := range groupHooks(group) {
			if entry, ok := h.(map[string]any); ok && isWashEntry(h) {
				return group, entry
			}
		}
		return group, nil
	}
	return nil, nil
}

func matcherOf(group map[string]any) string {
	s, _ := group["matcher"].(string)
	return s
}

func groupHooks(group map[string]any) []any {
	list, _ := group["hooks"].([]any)
	return list
}

// isWashEntry is the marked-entry test the removal path is built on.
func isWashEntry(h any) bool {
	entry, ok := h.(map[string]any)
	if !ok {
		return false
	}
	if t, _ := entry["type"].(string); t != "command" {
		return false
	}
	cmd, _ := entry["command"].(string)
	return strings.Contains(cmd, HookMarker)
}

func ensureMap(m map[string]any, k string) map[string]any {
	if sub, ok := m[k].(map[string]any); ok {
		return sub
	}
	sub := map[string]any{}
	m[k] = sub
	return sub
}

// ---- file I/O ----

// SettingsPath returns the Claude Code user settings file wash installs
// into: $CLAUDE_CONFIG_DIR/settings.json when set, else
// ~/.claude/settings.json.
func SettingsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

// LoadSettings decodes a settings file. A missing file is not an error —
// it decodes to an empty object, which install then creates.
//
// Numbers are kept as json.Number so re-encoding a value we never touched
// (a timeout, a port) writes back exactly what was there.
func LoadSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// EncodeSettings renders settings the way the agents' own tooling writes
// it: two-space indent, trailing newline.
func EncodeSettings(settings map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// SaveSettings writes settings back atomically (temp file in the same
// directory, then rename), preserving the file's existing mode. The
// first write also leaves a <path>.wash-bak copy of the original, so a
// user who dislikes what wash did to a hand-written file can put it back.
func SaveSettings(path string, settings map[string]any) error {
	data, err := EncodeSettings(settings)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		backup := path + ".wash-bak"
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if orig, err := os.ReadFile(path); err == nil {
				_ = os.WriteFile(backup, orig, mode)
			}
		}
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// HelperPath resolves the wash-agent-hook binary to write into the hook
// entries. A sibling of the running wash binary wins over $PATH: hooks
// outlive shells, and the wash the user is running now is the one whose
// helper should answer.
func HelperPath() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), HookMarker)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath(HookMarker); err == nil {
		return p
	}
	// Last resort: the bare name, resolved from the agent's $PATH at run
	// time. A wash terminal puts WASH_BIN_DIR on PATH, so this still
	// works for the common case.
	return HookMarker
}
