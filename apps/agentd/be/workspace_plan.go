package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/pkg/wire"
)

// The plan file is the plan as TOML, written by Wash whenever the plan
// changes, so a fresh orchestrator (or a person) reads it back rather than
// reconstructing it. Only Wash writes it while a workspace runs; a hand edit
// takes effect when plan_set {"from": …} loads it.

const planFileMarker = "# wash-plan: written by Wash as the plan changes."

type planFileDoc struct {
	Revision int64        `toml:"revision"`
	Legend   string       `toml:"legend,omitempty"`
	Nodes    []swarm.Node `toml:"node"`
}

type planFileState struct {
	Revision int64
	Info     os.FileInfo
	Status   agentproto.QADocumentStatus
}

// checkPlanFileTarget refuses a plan file path that holds something that is
// not Wash's plan: a symlink, a directory, someone's TOML.
func checkPlanFileTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("plan_file must be a regular file, not a symlink or directory")
	}
	if info.Size() == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head, _ := bufio.NewReader(io.LimitReader(f, 256)).ReadString('\n')
	if strings.TrimSpace(head) != planFileMarker {
		return errors.New("plan_file holds a file Wash did not write; load it with plan_set {\"from\": …} and choose another path to write to")
	}
	return nil
}

func encodePlanFile(w *swarm.Workspace) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintln(&b, planFileMarker)
	fmt.Fprintln(&b, "# Edit it only between workspaces; plan_set {\"from\": \"<this file>\"} loads it.")
	fmt.Fprintln(&b)
	nodes := w.Plan
	if nodes == nil {
		nodes = []swarm.Node{}
	}
	if err := toml.NewEncoder(&b).Encode(planFileDoc{Revision: w.PlanRevision, Legend: w.Legend, Nodes: nodes}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// readPlanFile reads nodes from a plan file: Wash's, or one written by hand
// in the same shape ([[node]] tables).
func readPlanFile(path string) ([]swarm.Node, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", errors.New("the plan file must be a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(b) > 4<<20 {
		return nil, "", errors.New("the plan file exceeds 4 MiB")
	}
	var doc planFileDoc
	meta, err := toml.Decode(string(b), &doc)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, "", fmt.Errorf("%s: unknown key %s", filepath.Base(path), undecoded[0])
	}
	return doc.Nodes, doc.Legend, nil
}

func writePlanFile(path string, content []byte) error {
	if err := checkPlanFileTarget(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".wash-plan-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// syncPlanFiles writes each workspace's plan file when its plan changed, or
// when the file was removed or replaced behind Wash's back.
func (ws *workspaceService) syncPlanFiles() {
	ws.qaMu.Lock()
	defer ws.qaMu.Unlock()
	if ws.planFiles == nil {
		ws.planFiles = map[string]*planFileState{}
	}
	for _, w := range ws.store.Shared().Workspaces {
		if w.PlanFile == "" {
			delete(ws.planFiles, w.ID)
			continue
		}
		st := ws.planFiles[w.ID]
		if st == nil || st.Status.Path != w.PlanFile {
			st = &planFileState{Revision: -1, Status: agentproto.QADocumentStatus{Path: w.PlanFile, State: "pending"}}
			ws.planFiles[w.ID] = st
		}
		info, statErr := os.Lstat(w.PlanFile)
		if st.Revision == w.PlanRevision && statErr == nil && st.Info != nil && os.SameFile(info, st.Info) && info.ModTime().Equal(st.Info.ModTime()) && info.Size() == st.Info.Size() {
			continue
		}
		content, err := encodePlanFile(&w)
		if err == nil {
			err = writePlanFile(w.PlanFile, content)
		}
		if err != nil {
			prior := st.Status
			st.Status = agentproto.QADocumentStatus{Path: w.PlanFile, State: "error", Error: err.Error(), Updated: prior.Updated}
			st.Revision = -1
			if ws.conn != nil && (prior.State != "error" || prior.Error != st.Status.Error) {
				desktop(ws.conn, agentproto.Notify{Title: w.Name + " · plan file not saved", Body: st.Status.Error + ". The plan is kept; Wash will retry.", Level: wire.NotifyLevelError})
			}
			continue
		}
		st.Revision = w.PlanRevision
		st.Info, _ = os.Lstat(w.PlanFile)
		st.Status = agentproto.QADocumentStatus{Path: w.PlanFile, State: "saved", Updated: time.Now().UnixMilli()}
	}
}

func (ws *workspaceService) planFileStatus(w *swarm.Workspace) agentproto.QADocumentStatus {
	if w == nil || w.PlanFile == "" {
		return agentproto.QADocumentStatus{State: "unconfigured"}
	}
	ws.qaMu.Lock()
	defer ws.qaMu.Unlock()
	if st := ws.planFiles[w.ID]; st != nil && st.Status.Path == w.PlanFile {
		return st.Status
	}
	return agentproto.QADocumentStatus{Path: w.PlanFile, State: "pending"}
}

// planLines is the plan one line per node, in tree order, children indented
// under their parent: id, state, title, what it still needs, and who is on
// it with what they are doing and how much context they have used (so a
// handoff can be timed at a commit boundary rather than wherever the
// warning lands).
func planLines(w *swarm.Workspace) []string {
	activity, _, usage := workspaceRuntime(w)
	var out []string
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, n := range w.Plan {
			if n.Parent != parent {
				continue
			}
			line := strings.Repeat("  ", depth)
			if n.Emoji != "" {
				line += n.Emoji + " "
			}
			line += n.ID + " · " + n.State + " · " + n.Title
			if n.Template != "" && n.Template != "package" {
				line += " [" + n.Template + "]"
			}
			if unmet := swarm.Unmet(w, n.ID); len(unmet) > 0 && n.State != "done" {
				line += " · needs " + strings.Join(unmet, ", ")
			}
			on := []string{}
			for _, m := range w.Members {
				if m.Node == n.ID && m.State != "ended" {
					doing := activity[m.ID]
					if u, ok := usage[m.ID]; ok && u.Size > 0 {
						doing += fmt.Sprintf(" · context %d%%", int(100*u.Used/u.Size))
					}
					on = append(on, m.Name+" ("+doing+")")
				}
			}
			if len(on) > 0 {
				line += " · on: " + strings.Join(on, ", ")
			}
			if len(n.Overrides) > 0 {
				line += fmt.Sprintf(" · %d override(s)", len(n.Overrides))
			}
			out = append(out, line)
			walk(n.ID, depth+1)
		}
	}
	walk("", 0)
	return out
}

// planGet answers plan_get: the summary lines by default; with node or
// detail, the nodes themselves, with their assignments and threads.
func (ws *workspaceService) planGet(w *swarm.Workspace, raw json.RawMessage) (any, error) {
	var p struct {
		Node   string `json:"node,omitempty"`
		Detail bool   `json:"detail,omitempty"`
	}
	if err := decodeWorkspace(raw, &p); err != nil {
		return nil, err
	}
	out := map[string]any{"revision": w.PlanRevision, "nodes": planLines(w)}
	if w.Legend != "" {
		out["legend"] = w.Legend
	}
	if w.PlanFile != "" {
		out["plan_file"] = w.PlanFile
	}
	if len(w.Plan) == 0 {
		out["hint"] = "The plan is empty. Add nodes with plan_set; every assignment is on a node."
	}
	if p.Node == "" && !p.Detail {
		return out, nil
	}
	details := []map[string]any{}
	for _, n := range w.Plan {
		if p.Node != "" && !swarm.Within(w, n.ID, p.Node) {
			continue
		}
		assignments := []map[string]string{}
		for _, a := range w.Assignments {
			if a.Node == n.ID {
				assignments = append(assignments, map[string]string{"id": a.ID, "member_id": a.Member, "state": a.State, "text": firstLine(a.Text, 120)})
			}
		}
		// With the revision a resolve or edit needs: it was in no read
		// tool's output, and orchestrators grepped the thread files for it.
		threads := []map[string]any{}
		for _, q := range w.QA {
			if q.Node == n.ID {
				threads = append(threads, map[string]any{"id": q.ID, "state": q.State, "title": firstLine(q.Title, 120), "assignee": q.Assignee, "revision": q.Revision})
			}
		}
		details = append(details, map[string]any{"node": n, "unmet": swarm.Unmet(w, n.ID), "assignments": assignments, "threads": threads})
	}
	if p.Node != "" && len(details) == 0 {
		return nil, fmt.Errorf("unknown node %s", p.Node)
	}
	out["detail"] = details
	return out, nil
}

// planSet answers plan_set: load a plan file (from), then upsert or delete
// nodes, in one transaction.
func (ws *workspaceService) planSet(ctx context.Context, h *hosted, raw json.RawMessage) (any, error) {
	var p struct {
		Nodes   map[string]json.RawMessage `json:"nodes,omitempty"`
		From    string                     `json:"from,omitempty"`
		Request string                     `json:"request_id,omitempty"`
	}
	if err := decodeWorkspace(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Nodes) == 0 && p.From == "" {
		return nil, errors.New("plan_set needs nodes, from, or both")
	}
	if len(p.Nodes) > 500 {
		return nil, errors.New("at most 500 nodes per call")
	}
	patches := map[string]*swarm.NodePatch{}
	for id, value := range p.Nodes {
		if string(value) == "null" {
			patches[id] = nil
			continue
		}
		var patch swarm.NodePatch
		if err := decodeWorkspace(value, &patch); err != nil {
			return nil, fmt.Errorf("node %s: %w", id, err)
		}
		patches[id] = &patch
	}
	w := ws.store.View(h.sessionID)
	if w == nil {
		return nil, errors.New("no workspace; call workspace_configure")
	}
	var loaded []swarm.Node
	legend := ""
	if p.From != "" {
		path := p.From
		if !filepath.IsAbs(path) {
			path = filepath.Join(w.Root, path)
		}
		path, err := h.confineOrAsk(ctx, "Read", path)
		if err != nil {
			return nil, err
		}
		if loaded, legend, err = readPlanFile(path); err != nil {
			return nil, err
		}
	}
	return ws.store.Transaction(h.sessionID, "plan_set", p.Request, raw, false, func(s *swarm.Store) (any, error) {
		var out any
		err := s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
			if p.From != "" {
				if err := swarm.ReplacePlan(w, m, loaded); err != nil {
					return err
				}
				if legend != "" {
					w.Legend = legend
				}
			}
			if len(patches) > 0 {
				if err := swarm.ApplyPlan(w, m, patches); err != nil {
					return err
				}
			}
			out = map[string]any{"revision": w.PlanRevision, "nodes": planLines(w)}
			return nil
		})
		return out, err
	})
}

// planAccept answers plan_accept: the node done, its trailer block, and the
// files to stage with the merge.
func (ws *workspaceService) planAccept(h *hosted, raw json.RawMessage) (any, error) {
	var p struct {
		Node    string       `json:"node"`
		Gates   []swarm.Gate `json:"gates,omitempty"`
		Request string       `json:"request_id,omitempty"`
	}
	if err := decodeWorkspace(raw, &p); err != nil {
		return nil, err
	}
	return ws.store.Transaction(h.sessionID, "plan_accept", p.Request, raw, false, func(s *swarm.Store) (any, error) {
		var out map[string]any
		err := s.Mutate(h.sessionID, false, func(w *swarm.Workspace, m *swarm.Member) error {
			accepted, err := swarm.Accept(w, m, p.Node, p.Gates)
			if err != nil {
				return err
			}
			stage := []string{}
			rel := func(path string) string {
				if r, err := filepath.Rel(w.Root, path); err == nil && !strings.HasPrefix(r, "..") {
					return r
				}
				return path
			}
			if w.PlanFile != "" {
				stage = append(stage, rel(w.PlanFile))
			}
			if w.QADir != "" {
				for _, id := range accepted.Threads {
					stage = append(stage, rel(filepath.Join(w.QADir, id+".md")))
				}
			}
			slices.Sort(stage)
			out = map[string]any{"node": accepted.Node, "trailers": accepted.Trailers, "stage": stage, "instruction": "End the node's merge commit message with the trailers as its last paragraph, one per line exactly as given (e.g. git commit -m <subject> -m <trailers>, the trailers keeping their newlines), and stage the listed files with it."}
			return nil
		})
		return out, err
	})
}
