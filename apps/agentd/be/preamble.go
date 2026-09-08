// The initial prompt: a block of text sent to every NEW session before
// anything you type.
//
// It is the answer to retyping the same preamble — "you are working in
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
//     — there is no instructions field to put a preamble in. So it rides
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

// preambleFileName is the file, beside agents.json in the wash config dir.
const preambleFileName = "agent-preamble.txt"

// maxPreambleBytes bounds what the FE can store. Generous — a page of
// instructions is a few KB — but finite, because this text is prepended
// to a prompt and an unbounded blob is a way to make every session
// expensive by accident.
const maxPreambleBytes = 64 << 10

// preamblePath is $XDG_CONFIG_HOME/wash/agent-preamble.txt, or "" when no
// home is resolvable. Same resolution as agentpolicy.Path, deliberately:
// two agent settings in two different directories would be a small
// cruelty to whoever goes looking for them.
func preamblePath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "wash", preambleFileName)
}

// loadPreamble reads the stored initial prompt. Any problem — missing,
// unreadable — is the empty string, which means "no preamble": a broken
// or absent file must never stop a session starting.
func loadPreamble() string {
	path := preamblePath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxPreambleBytes {
		data = data[:maxPreambleBytes]
	}
	return strings.TrimSpace(string(data))
}

// savePreamble writes the initial prompt atomically (temp file in the same
// directory, then rename), so a session starting mid-save reads either the
// old text or the new one and never half of either. Saving an empty
// string removes the file — "no preamble" and "an empty preamble" are the
// same thing, and leaving an empty file behind would make the difference
// look meaningful.
func savePreamble(text string) error {
	path := preamblePath()
	if path == "" {
		return nil
	}
	text = strings.TrimSpace(text)
	if len(text) > maxPreambleBytes {
		text = text[:maxPreambleBytes]
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
	tmp, err := os.CreateTemp(dir, ".agent-preamble-*.txt")
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

// withPreamble is what a new session's first prompt becomes.
//
// The preamble goes FIRST and is separated by a blank line, so the agent
// reads standing instructions before the request they apply to. With no
// prompt of your own it is sent alone — starting a session with only a
// preamble is a legitimate way to set the scene and then talk.
func withPreamble(preamble, prompt string) string {
	preamble = strings.TrimSpace(preamble)
	prompt = strings.TrimSpace(prompt)
	switch {
	case preamble == "":
		return prompt
	case prompt == "":
		return preamble
	default:
		return preamble + "\n\n" + prompt
	}
}
