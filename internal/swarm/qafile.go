package swarm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// A QA thread's file, <thread>.md in the workspace's QA directory: a marker
// line naming the thread, the thread as Markdown for people and diffs, and a
// checkpoint (the thread as JSON) from which a later workspace resumes it
// without the store that wrote it. One file per thread, so a resolved thread
// stops changing and a commit's diff shows only the threads it touched.

// QAFileMarker is a thread file's first line. A file without it is never
// overwritten: it is someone's document, not Wash's.
func QAFileMarker(id string) string { return "<!-- wash-qa-thread: " + id + " -->" }

const qaFileCheckpoint = "<!-- wash-qa-checkpoint-v2: "

// QAFile is what a thread file holds: the thread, the names of its authors,
// and owner decisions still pending on it, which a resumed workspace asks
// again.
type QAFile struct {
	Thread    QAThread          `json:"thread"`
	Authors   map[string]string `json:"authors"`
	Decisions []Message         `json:"decisions,omitempty"`
}

// QAFileFor is thread id's file content as w holds it.
func QAFileFor(w *Workspace, id string) QAFile {
	q := QA(w, id)
	f := QAFile{Thread: *q, Authors: map[string]string{}}
	name := qaNames(w)
	add := func(id string) {
		if id != "" && id != "human" {
			f.Authors[id] = name(id)
		}
	}
	add(q.Creator)
	add(q.Assignee)
	for _, id := range q.Participants {
		add(id)
	}
	for _, e := range q.Events {
		add(e.Author)
	}
	for _, m := range w.Messages {
		if m.Thread == id && m.Type == "decision_request" && m.State == "recorded" {
			f.Decisions = append(f.Decisions, m)
			add(m.Sender)
		}
	}
	return f
}

// ShortenPaths writes the project root as "." and the home directory as "~",
// so a committed thread file names no user's machine.
func ShortenPaths(s, root, home string) string {
	if root != "" && root != "/" {
		s = strings.ReplaceAll(s, filepath.Clean(root)+"/", "./")
		s = strings.ReplaceAll(s, filepath.Clean(root), ".")
	}
	if home != "" && home != "/" {
		s = strings.ReplaceAll(s, filepath.Clean(home)+"/", "~/")
		s = strings.ReplaceAll(s, filepath.Clean(home), "~")
	}
	return s
}

// EncodeQAFile renders a thread file, with paths shortened in everything it
// carries: the Markdown and the checkpoint say the same thing.
func EncodeQAFile(f QAFile, root, home string) ([]byte, error) {
	short := func(s string) string { return ShortenPaths(s, root, home) }
	q := f.Thread
	q.Title, q.Evidence = short(q.Title), short(q.Evidence)
	q.DecisionRefs = append([]string(nil), q.DecisionRefs...)
	for i := range q.DecisionRefs {
		q.DecisionRefs[i] = short(q.DecisionRefs[i])
	}
	q.Events = append([]QAEvent(nil), q.Events...)
	for i := range q.Events {
		q.Events[i].Body = short(q.Events[i].Body)
	}
	f.Thread = q
	f.Decisions = append([]Message(nil), f.Decisions...)
	for i := range f.Decisions {
		f.Decisions[i].Body = short(f.Decisions[i].Body)
	}
	var b bytes.Buffer
	fmt.Fprintln(&b, QAFileMarker(q.ID))
	name := func(id string) string {
		if id == "human" {
			return "Owner"
		}
		if n := f.Authors[id]; n != "" {
			return n
		}
		return id
	}
	if err := WriteQAThread(&b, q, name, false); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "\n%s%s -->\n", qaFileCheckpoint, base64.StdEncoding.EncodeToString(payload))
	return b.Bytes(), nil
}

// IsQAFile says whether b starts with a thread file's marker.
func IsQAFile(b []byte) bool { return bytes.HasPrefix(b, []byte("<!-- wash-qa-thread: ")) }

// DecodeQAFile reads a thread file back. A file that is Wash's but damaged
// is an error, never an empty thread.
func DecodeQAFile(b []byte) (*QAFile, error) {
	if !IsQAFile(b) {
		return nil, errors.New("not a Wash QA thread file")
	}
	i := bytes.LastIndex(b, []byte("\n"+qaFileCheckpoint))
	if i < 0 {
		return nil, errors.New("QA thread file has no checkpoint")
	}
	payload := strings.TrimSpace(string(b[i+1+len(qaFileCheckpoint):]))
	if !strings.HasSuffix(payload, " -->") {
		return nil, errors.New("incomplete QA checkpoint")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(payload, " -->"))
	if err != nil {
		return nil, fmt.Errorf("invalid QA checkpoint: %w", err)
	}
	var f QAFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("invalid QA checkpoint: %w", err)
	}
	q := f.Thread
	if !ValidProfileName(q.ID) || q.Revision < 1 || len(q.Events) > 1000 || !bytes.HasPrefix(b, []byte(QAFileMarker(q.ID)+"\n")) {
		return nil, errors.New("QA checkpoint does not match its thread")
	}
	for _, d := range f.Decisions {
		if d.Thread != q.ID || d.Type != "decision_request" {
			return nil, errors.New("QA checkpoint decision belongs to another thread")
		}
	}
	return &f, nil
}
