// The default prompt: a block of text sent to every NEW session before
// anything you type.
//
// It is the answer to retyping the same default prompt — "you are working in
// this repo, read CLAUDE.md, prefer X over Y" — into every fresh agent.
// Stored once, applied automatically, and visible while it is doing so.
//
// Three decisions worth stating, because each has a plausible alternative:
//
//   - **A file, not a per-window setting.** It belongs to the person, not
//     to a browser tab, so it lives beside the approval policy in
//     $XDG_CONFIG_HOME/wash and survives reloads, restarts, and the
//     machine. It is plain text rather than JSON because that is what it
//     IS — a paragraph — and a config file a human can edit in an editor
//     without escaping newlines is a better config file.
//
//   - **Sent as a prompt, not as protocol.** This version of ACP's
//     session/new carries only cwd and MCP servers (internal/acp/types.go)
//     — there is no instructions field to put a default prompt in. So it rides
//     as the first prompt, which also means it appears in the transcript:
//     an instruction the agent was given must be one you can read back.
//
//   - **New sessions only.** It is an *initial* prompt. Resuming or
//     reattaching to a session that already heard it must not say it
//     again, and prepending it to every prompt would be a different
//     feature (and would quietly triple the cost of a long conversation).

package agentd

import (
	"os"
	"path/filepath"
	"strings"
)

// defaultPromptFileName is the file, beside agents.json in the wash config dir.
const defaultPromptFileName = "agent-default-prompt.txt"

// maxDefaultPromptBytes bounds what the FE can store. Generous — a page of
// instructions is a few KB — but finite, because this text is prepended
// to a prompt and an unbounded blob is a way to make every session
// expensive by accident.
const maxDefaultPromptBytes = 64 << 10

// defaultPromptPath is $XDG_CONFIG_HOME/wash/agent-default-prompt.txt, or "" when no
// home is resolvable. Same resolution as agentpolicy.Path, deliberately:
// two agent settings in two different directories would be a small
// cruelty to whoever goes looking for them.
func defaultPromptPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "wash", defaultPromptFileName)
}

// loadDefaultPrompt reads the stored default prompt. Any problem — missing,
// unreadable — is the empty string, which means "no default prompt": a broken
// or absent file must never stop a session starting.
func loadDefaultPrompt() string {
	path := defaultPromptPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxDefaultPromptBytes {
		data = data[:maxDefaultPromptBytes]
	}
	return strings.TrimSpace(string(data))
}

// saveDefaultPrompt writes the default prompt atomically (temp file in the same
// directory, then rename), so a session starting mid-save reads either the
// old text or the new one and never half of either. Saving an empty
// string removes the file — "no default prompt" and "an empty default prompt" are the
// same thing, and leaving an empty file behind would make the difference
// look meaningful.
func saveDefaultPrompt(text string) error {
	path := defaultPromptPath()
	if path == "" {
		return nil
	}
	text = strings.TrimSpace(text)
	if len(text) > maxDefaultPromptBytes {
		text = text[:maxDefaultPromptBytes]
	}
	if text == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent-default-prompt-*.txt")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(text + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// refreshDefaultPrompt re-derives the "a default prompt will be sent" flag
// from the file and reports whether it moved. Called inside Mutate, on the
// sweep.
//
// The prompt is stored as plain text precisely so a person can edit it in
// an editor, which makes the file the truth: a flag remembered from the
// last save through the dialog goes stale the moment anyone writes the
// file directly, and the launcher then claims a prompt that isn't there
// (or hides one that is) until agentd restarts. Re-reading a bounded file
// every sweep is cheaper than that lie.
func refreshDefaultPrompt(s *State) bool {
	has := loadDefaultPrompt() != ""
	if has == s.HasDefaultPrompt {
		return false
	}
	s.HasDefaultPrompt = has
	return true
}

// withDefaultPrompt is what a new session's first prompt becomes.
//
// The default prompt goes FIRST and is separated by a blank line, so the agent
// reads standing instructions before the request they apply to. With no
// prompt of your own it is sent alone — starting a session with only a
// default prompt is a legitimate way to set the scene and then talk.
func withDefaultPrompt(preset, prompt string) string {
	preset = strings.TrimSpace(preset)
	prompt = strings.TrimSpace(prompt)
	switch {
	case preset == "":
		return prompt
	case prompt == "":
		return preset
	default:
		return preset + "\n\n" + prompt
	}
}
