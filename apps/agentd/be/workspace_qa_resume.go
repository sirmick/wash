package agentd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sirmick/wash/internal/swarm"
)

// The visible Markdown is accompanied by a versioned, lossless checkpoint. A
// project QA file can move between installations without the original store.
const qaCheckpoint = "<!-- wash-qa-checkpoint-v1: "
const maxQAFileBytes = 64 << 20

type qaArchive struct {
	OriginalHash string            `json:"-"`
	DocumentID   string            `json:"document_id"`
	Threads      []swarm.QAThread  `json:"threads"`
	Authors      map[string]string `json:"authors"`
	Preamble     string            `json:"preamble,omitempty"`
	Decisions    []swarm.Message   `json:"decisions,omitempty"`
}

func qaOwner(w *swarm.Workspace) string {
	if w.QADocumentID != "" {
		return w.QADocumentID
	}
	return w.ID
}
func archiveQA(w *swarm.Workspace) qaArchive {
	a := qaArchive{DocumentID: qaOwner(w), Threads: w.QA, Preamble: w.QAPreamble, Authors: map[string]string{}}
	for id, name := range w.QAAuthors {
		a.Authors[id] = name
	}
	for _, m := range w.Members {
		a.Authors[m.ID] = m.Name
	}
	for _, m := range w.Messages {
		if m.Type == "decision_request" && m.State == "recorded" && m.Thread != "" {
			a.Decisions = append(a.Decisions, m)
		}
	}
	return a
}
func writeQACheckpoint(out io.Writer, w *swarm.Workspace) error {
	b, err := json.Marshal(archiveQA(w))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "\n%s%s -->\n", qaCheckpoint, base64.StdEncoding.EncodeToString(b))
	return err
}
func readQAArchive(path string) (*qaArchive, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("QA document must be a regular file, not a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("QA document changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxQAFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxQAFileBytes {
		return nil, errors.New("QA document exceeds 64 MiB restore limit")
	}
	if len(b) == 0 {
		return nil, nil
	}
	if i := bytes.LastIndex(b, []byte("\n"+qaCheckpoint)); i >= 0 {
		payload := strings.TrimSpace(string(b[i+1+len(qaCheckpoint):]))
		if !strings.HasSuffix(payload, " -->") {
			return nil, errors.New("incomplete QA checkpoint; original file preserved")
		}
		encoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(payload, " -->"))
		if err != nil {
			return nil, fmt.Errorf("invalid QA checkpoint: %w", err)
		}
		var a qaArchive
		if err := json.Unmarshal(encoded, &a); err != nil {
			return nil, err
		}
		if a.DocumentID == "" || !bytes.HasPrefix(b, []byte(qaFileMarker(a.DocumentID)+"\n")) {
			return nil, errors.New("QA checkpoint identity mismatch")
		}
		if len(a.Threads) > 500 {
			return nil, errors.New("QA checkpoint exceeds thread limit")
		}
		seen := map[string]bool{}
		for _, q := range a.Threads {
			if !swarm.ValidProfileName(q.ID) || seen[q.ID] || q.Revision < 1 || len(q.Events) > 1000 {
				return nil, errors.New("invalid QA checkpoint thread")
			}
			seen[q.ID] = true
		}
		a.OriginalHash = fmt.Sprintf("%x", sha256.Sum256(b))
		return &a, nil
	}
	if bytes.Contains(b, []byte("<!-- wash-qa-checkpoint")) {
		return nil, errors.New("unsupported or damaged QA checkpoint; original file preserved")
	}
	// Ordinary Markdown and older exports remain visible verbatim. Old exports
	// with a matching durable store are upgraded from that store by configure.
	return &qaArchive{OriginalHash: fmt.Sprintf("%x", sha256.Sum256(b)), Preamble: string(b), Authors: map[string]string{}}, nil
}
func restoreQA(w *swarm.Workspace, a *qaArchive) error {
	if len(w.QA) > 0 || w.QAPreamble != "" {
		return errors.New("workspace already has QA history; resume the file in a new workspace")
	}
	w.QA, w.QAPreamble, w.QAAuthors = a.Threads, a.Preamble, a.Authors
	if a.DocumentID != "" {
		w.QADocumentID = a.DocumentID
	}
	// Previous sessions are historical identities, never launch instructions.
	// The new orchestrator owns unfinished questions until it assigns its team.
	for i := range w.QA {
		q := &w.QA[i]
		if q.State != "resolved" {
			rev := q.Revision
			priorState := q.State
			_, err := swarm.UpdateQA(w, swarm.GetMember(w, w.Lead), swarm.QAUpdate{ID: q.ID, Action: "assign", Expected: &rev, Assignee: w.Lead, Body: "Resumed from QA document; orchestrator will assign the current team."})
			if err != nil {
				return err
			}
			if priorState == "blocked" {
				q.State = "blocked"
			}
		}
	}
	for _, msg := range a.Decisions {
		if swarm.QA(w, msg.Thread) == nil {
			return errors.New("QA checkpoint decision references missing thread")
		}
		msg.Swarm, msg.Sender, msg.Recipient, msg.State = w.ID, w.Lead, "human", "recorded"
		w.Messages = append(w.Messages, msg)
		swarm.QA(w, msg.Thread).State = "awaiting-owner"
	}
	return nil
}
