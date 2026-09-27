package swarm

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The plan is the workspace's backbone: a graph of nodes that every
// assignment and member hangs off. Its structure is the orchestrator's to
// choose (milestones, packages, steps, notes, drawn by template); what Wash
// enforces is that the plan is true: no work happens off it, work starts
// only when what it needs is done (or the start records why not), and a
// node is done only when the orchestrator says so with nothing open on it.

// Node is one plan node.
type Node struct {
	ID    string `json:"id" toml:"id"`
	Title string `json:"title" toml:"title"`
	Emoji string `json:"emoji,omitempty" toml:"emoji,omitempty"`
	// Template is how the node is drawn: milestone, package, step or note.
	// Wash gives it no other meaning, except that a milestone with no
	// children is a sketch.
	Template string `json:"template,omitempty" toml:"template,omitempty"`
	// Parent is the node this one sits inside.
	Parent string `json:"parent,omitempty" toml:"parent,omitempty"`
	// Needs are node IDs that must be done, or QA thread IDs that must be
	// resolved, before work on this node (or anything inside it) starts.
	Needs []string `json:"needs,omitempty" toml:"needs,omitempty"`
	Body  string   `json:"body,omitempty" toml:"body,omitempty"`
	// State is todo, active, reported, done, failed, or a short word of the
	// orchestrator's own.
	State    string `json:"state" toml:"state"`
	Revision int64  `json:"revision" toml:"revision"`
	// Overrides record each start with needs unmet, and why.
	Overrides []string `json:"overrides,omitempty" toml:"overrides,omitempty"`
}

// NodePatch changes a node; nil fields stay.
type NodePatch struct {
	Title    *string   `json:"title,omitempty"`
	Emoji    *string   `json:"emoji,omitempty"`
	Template *string   `json:"template,omitempty"`
	Parent   *string   `json:"parent,omitempty"`
	Needs    *[]string `json:"needs,omitempty"`
	Body     *string   `json:"body,omitempty"`
	State    *string   `json:"state,omitempty"`
	Expected *int64    `json:"expected_revision,omitempty"`
}

// PlanStates are the states Wash itself sets or reads.
var PlanStates = []string{"todo", "active", "reported", "done", "failed"}

// PlanTemplates are the ways a node can be drawn.
var PlanTemplates = []string{"milestone", "package", "step", "note"}

const maxNodes = 500

// PlanNode is the node with id, or nil.
func PlanNode(w *Workspace, id string) *Node {
	for i := range w.Plan {
		if w.Plan[i].ID == id {
			return &w.Plan[i]
		}
	}
	return nil
}

// Open is whether an assignment is still to be done.
func (a Assignment) Open() bool {
	return a.State == "assigned" || a.State == "active" || a.State == "blocked"
}

// OpenOn lists the open assignments on node id.
func OpenOn(w *Workspace, id string) []Assignment {
	var out []Assignment
	for _, a := range w.Assignments {
		if a.Node == id && a.Open() {
			out = append(out, a)
		}
	}
	return out
}

// Within is whether node id is ancestor or sits (at any depth) inside it.
func Within(w *Workspace, id, ancestor string) bool {
	for seen := 0; id != "" && seen <= len(w.Plan); seen++ {
		if id == ancestor {
			return true
		}
		n := PlanNode(w, id)
		if n == nil {
			return false
		}
		id = n.Parent
	}
	return false
}

func children(w *Workspace, id string) []string {
	var out []string
	for _, n := range w.Plan {
		if n.Parent == id {
			out = append(out, n.ID)
		}
	}
	return out
}

// Unmet lists what node id is still waiting for: its own needs and those of
// every node it sits inside. A node need is met when that node is done; a
// QA need when the thread is resolved.
func Unmet(w *Workspace, id string) []string {
	var out []string
	for seen := 0; id != "" && seen <= len(w.Plan); seen++ {
		n := PlanNode(w, id)
		if n == nil {
			break
		}
		for _, need := range n.Needs {
			if dep := PlanNode(w, need); dep != nil {
				if dep.State != "done" {
					out = append(out, need+" ("+dep.State+")")
				}
			} else if q := QA(w, need); q != nil && q.State != "resolved" {
				out = append(out, "QA "+need+" ("+q.State+")")
			}
		}
		id = n.Parent
	}
	return out
}

func validState(s string) bool {
	if slices.Contains(PlanStates, s) {
		return true
	}
	if len(s) == 0 || len(s) > 24 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '-') {
			return false
		}
	}
	return true
}

// ValidatePlan checks the whole plan: IDs, fields, references and cycles.
func ValidatePlan(w *Workspace) error {
	if len(w.Plan) > maxNodes {
		return fmt.Errorf("a plan holds at most %d nodes", maxNodes)
	}
	seen := map[string]bool{}
	for _, n := range w.Plan {
		switch {
		case !ValidProfileName(n.ID):
			return fmt.Errorf("node id %q: 1–80 letters, digits, - or _", n.ID)
		case seen[n.ID]:
			return fmt.Errorf("node %s appears twice", n.ID)
		case !ValidText(n.Title, 200) || strings.ContainsAny(n.Title, "\n\r"):
			return fmt.Errorf("node %s: title is one line of at most 200 bytes", n.ID)
		case len(n.Emoji) > 64:
			return fmt.Errorf("node %s: emoji too long", n.ID)
		case n.Template != "" && !slices.Contains(PlanTemplates, n.Template):
			return fmt.Errorf("node %s: template is one of %s", n.ID, strings.Join(PlanTemplates, ", "))
		case len(n.Body) > ReportLimit:
			return fmt.Errorf("node %s: body is at most %d bytes (got %d); link to a page for detail", n.ID, ReportLimit, len(n.Body))
		case !validState(n.State):
			return fmt.Errorf("node %s: state is %s, or a short lower-case word", n.ID, strings.Join(PlanStates, ", "))
		case len(n.Needs) > 32:
			return fmt.Errorf("node %s: at most 32 needs", n.ID)
		}
		seen[n.ID] = true
	}
	for _, n := range w.Plan {
		if n.Parent != "" {
			if n.Parent == n.ID || !seen[n.Parent] {
				return fmt.Errorf("node %s: parent %q is not another node", n.ID, n.Parent)
			}
		}
		for _, need := range n.Needs {
			if !seen[need] && QA(w, need) == nil {
				return fmt.Errorf("node %s: needs %q, which is neither a node nor a QA thread", n.ID, need)
			}
			if seen[need] && (Within(w, n.ID, need) || Within(w, need, n.ID)) {
				return fmt.Errorf("node %s: cannot need %s, which it sits inside or which sits inside it", n.ID, need)
			}
		}
	}
	for _, n := range w.Plan {
		id, steps := n.ID, 0
		for id != "" && steps <= len(w.Plan) {
			id = PlanNode(w, id).Parent
			steps++
		}
		if id != "" {
			return fmt.Errorf("node %s: its parents form a cycle", n.ID)
		}
	}
	// Needs between nodes must not form a cycle.
	state := map[string]int{}
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("needs form a cycle: %s", strings.Join(append(path, id), " → "))
		case 2:
			return nil
		}
		state[id] = 1
		for _, need := range PlanNode(w, id).Needs {
			if PlanNode(w, need) != nil {
				if err := visit(need, append(path, id)); err != nil {
					return err
				}
			}
		}
		state[id] = 2
		return nil
	}
	for _, n := range w.Plan {
		if err := visit(n.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPlan upserts and deletes nodes (a nil patch deletes). The
// orchestrator may change anything; the Architect may add and edit nodes
// that have not started (todo, nothing open on them) and leave them todo.
// Only the orchestrator sets done, and only with nothing open on the node.
func ApplyPlan(w *Workspace, m *Member, patches map[string]*NodePatch) error {
	lead := m.ID == w.Lead
	if !lead && m.Role != "architect" {
		return errors.New("the plan is the orchestrator's (and the Architect's, for nodes not started)")
	}
	ids := make([]string, 0, len(patches))
	for id := range patches {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	next := w.PlanRevision + 1
	for _, id := range ids {
		p := patches[id]
		n := PlanNode(w, id)
		if !lead && n != nil && (n.State != "todo" || len(OpenOn(w, id)) > 0) {
			return fmt.Errorf("node %s has started (%s); changing it is the orchestrator's", id, n.State)
		}
		if p != nil && p.Expected != nil && (n == nil || *p.Expected != n.Revision) {
			current := int64(0)
			if n != nil {
				current = n.Revision
			}
			return fmt.Errorf("node %s revision conflict: expected %d, current %d; read it again with plan_get", id, *p.Expected, current)
		}
		if p == nil {
			if n == nil {
				return fmt.Errorf("unknown node %s", id)
			}
			if open := OpenOn(w, id); len(open) > 0 {
				return fmt.Errorf("node %s has %d open assignments; finish or cancel them first", id, len(open))
			}
			if kids := children(w, id); len(kids) > 0 {
				return fmt.Errorf("node %s holds %s; move or delete them first", id, strings.Join(kids, ", "))
			}
			for _, other := range w.Plan {
				if slices.Contains(other.Needs, id) {
					return fmt.Errorf("node %s is needed by %s", id, other.ID)
				}
			}
			for _, mem := range w.Members {
				if mem.Node == id && mem.State != "ended" {
					return fmt.Errorf("member %s is on node %s", mem.Name, id)
				}
			}
			i := slices.IndexFunc(w.Plan, func(x Node) bool { return x.ID == id })
			w.Plan = slices.Delete(w.Plan, i, i+1)
			continue
		}
		if n == nil {
			w.Plan = append(w.Plan, Node{ID: id, State: "todo"})
			n = &w.Plan[len(w.Plan)-1]
		}
		if p.Title != nil {
			n.Title = *p.Title
		}
		if p.Emoji != nil {
			n.Emoji = *p.Emoji
		}
		if p.Template != nil {
			n.Template = *p.Template
		}
		if p.Parent != nil {
			n.Parent = *p.Parent
		}
		if p.Needs != nil {
			n.Needs = slices.Clone(*p.Needs)
		}
		if p.Body != nil {
			n.Body = *p.Body
		}
		if p.State != nil {
			if !lead && *p.State != "todo" {
				return fmt.Errorf("node %s: the Architect plans nodes (todo); the orchestrator moves them", id)
			}
			if *p.State == "done" && n.State != "done" {
				if open := OpenOn(w, id); len(open) > 0 {
					return fmt.Errorf("node %s has %d open assignments; it is done when they are", id, len(open))
				}
			}
			n.State = *p.State
		}
		n.Revision = next
	}
	if err := ValidatePlan(w); err != nil {
		return err
	}
	w.PlanRevision = next
	PlanNudges(w)
	return nil
}

// ReplacePlan swaps the whole plan for nodes (a plan file loaded), refused
// while work is open on the current one.
func ReplacePlan(w *Workspace, m *Member, nodes []Node) error {
	if m.ID != w.Lead {
		return errors.New("loading a plan is the orchestrator's")
	}
	for _, a := range w.Assignments {
		if a.Open() && a.Node != "" {
			return fmt.Errorf("assignment %s is open on node %s; a plan loads only with no work open", a.ID, a.Node)
		}
	}
	next := w.PlanRevision + 1
	w.Plan = slices.Clone(nodes)
	for i := range w.Plan {
		if w.Plan[i].State == "" {
			w.Plan[i].State = "todo"
		}
		w.Plan[i].Revision = next
	}
	if err := ValidatePlan(w); err != nil {
		return err
	}
	for _, mem := range w.Members {
		if mem.Node != "" && mem.State != "ended" && PlanNode(w, mem.Node) == nil {
			return fmt.Errorf("member %s is on node %s, which the loaded plan does not have", mem.Name, mem.Node)
		}
	}
	w.PlanRevision = next
	return nil
}

// startWork puts an assignment on its node: the node must exist, what it
// needs must be done unless override says why not (recorded on the node),
// and the node becomes active.
func startWork(w *Workspace, by *Member, node, override string) error {
	n := PlanNode(w, node)
	if n == nil {
		return fmt.Errorf("unknown node %q: every assignment is on a plan node (plan_set adds one)", node)
	}
	if unmet := Unmet(w, node); len(unmet) > 0 {
		if strings.TrimSpace(override) == "" {
			return fmt.Errorf("node %s needs %s first; start anyway with override:\"<reason>\"", node, strings.Join(unmet, ", "))
		}
		if len(override) > 500 {
			return errors.New("override reason is at most 500 bytes")
		}
		if len(n.Overrides) >= 32 {
			return fmt.Errorf("node %s has 32 recorded overrides", node)
		}
		n.Overrides = append(n.Overrides, fmt.Sprintf("%s: started by %s before %s: %s", time.Now().UTC().Format("2006-01-02"), by.Name, strings.Join(unmet, ", "), override))
	}
	if n.State != "active" {
		n.State = "active"
		n.Revision = w.PlanRevision + 1
		w.PlanRevision++
	}
	return nil
}

// settleWork moves an assignment's node when the assignment resolves: with
// nothing else open on it, a result makes it reported and a failure failed.
func settleWork(w *Workspace, node string, failed bool) {
	n := PlanNode(w, node)
	if n == nil || len(OpenOn(w, node)) > 0 || n.State != "active" {
		return
	}
	n.State = "reported"
	if failed {
		n.State = "failed"
	}
	n.Revision = w.PlanRevision + 1
	w.PlanRevision++
}

// nudge sends the orchestrator one lifecycle message per key.
func nudge(w *Workspace, key, body string) {
	if slices.Contains(w.Nudged, key) {
		return
	}
	if _, err := AddMessage(w, "wash", w.Lead, "lifecycle", body, "", "", ""); err == nil {
		w.Nudged = append(w.Nudged, key)
	}
}

// PlanNudges tells the orchestrator when a milestone's nodes are all done,
// and when the milestone that follows a done one is still a sketch.
func PlanNudges(w *Workspace) {
	for _, n := range w.Plan {
		if n.Template != "milestone" {
			continue
		}
		kids := children(w, n.ID)
		if n.State != "done" && len(kids) > 0 && !slices.ContainsFunc(kids, func(id string) bool { return PlanNode(w, id).State != "done" }) {
			nudge(w, "complete:"+n.ID, "Every node in milestone "+n.ID+" ("+n.Title+") is done: accept it with plan_accept, or set it done.")
		}
		if n.State != "done" {
			continue
		}
		for _, next := range w.Plan {
			if next.Template == "milestone" && slices.Contains(next.Needs, n.ID) && len(children(w, next.ID)) == 0 {
				nudge(w, "sketch:"+next.ID, "Milestone "+n.ID+" is done and "+next.ID+" ("+next.Title+") is next, still a sketch: plan it with plan_set.")
			}
		}
	}
}

// Gate is a check run before acceptance, and how it exited.
type Gate struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
}

// Accepted is what accepting a node gives the orchestrator: the trailer
// block for the merge commit, and the QA threads it names.
type Accepted struct {
	Node     string   `json:"node"`
	Trailers string   `json:"trailers"`
	Threads  []string `json:"threads"`
}

// Accept sets a node done and writes its evidence as commit trailers: the
// QA threads about it, the gates as run, and each reviewer's verdict (the
// first line of its latest result on the node). Evidence lives on the commit
// it vouches for, generated rather than typed.
func Accept(w *Workspace, m *Member, id string, gates []Gate) (Accepted, error) {
	var out Accepted
	if m.ID != w.Lead {
		return out, errors.New("accepting a node is the orchestrator's")
	}
	n := PlanNode(w, id)
	if n == nil {
		return out, fmt.Errorf("unknown node %s", id)
	}
	for _, a := range w.Assignments {
		if a.Open() && a.Node != "" && Within(w, a.Node, id) {
			return out, fmt.Errorf("assignment %s is open on %s; accept when it is done", a.ID, a.Node)
		}
	}
	if len(gates) > 32 {
		return out, errors.New("at most 32 gates")
	}
	lines := []string{"Plan-Node: " + id}
	for _, q := range w.QA {
		if q.Node != "" && Within(w, q.Node, id) {
			out.Threads = append(out.Threads, q.ID)
		}
	}
	if len(out.Threads) > 0 {
		lines = append(lines, "QA: "+strings.Join(out.Threads, ", "))
	}
	if len(gates) > 0 {
		parts := []string{}
		for _, g := range gates {
			if !ValidText(g.Command, 200) || strings.ContainsAny(g.Command, "\n\r") {
				return out, errors.New("a gate's command is one line of at most 200 bytes")
			}
			parts = append(parts, fmt.Sprintf("%s %d", g.Command, g.ExitCode))
		}
		lines = append(lines, "Gates: "+strings.Join(parts, ", "))
	}
	verdicts := map[string]string{}
	order := []string{}
	for _, a := range w.Assignments {
		r := GetMember(w, a.Member)
		if a.State != "completed" || a.Node == "" || !Within(w, a.Node, id) || r == nil || r.Role != "reviewer" {
			continue
		}
		if _, ok := verdicts[r.ID]; !ok {
			order = append(order, r.ID)
		}
		line, _, _ := strings.Cut(strings.TrimSpace(a.Result), "\n")
		if len(line) > 120 {
			line = strings.ToValidUTF8(line[:120], "") + "…"
		}
		verdicts[r.ID] = r.Name + ": " + line
	}
	for _, rid := range order {
		lines = append(lines, "Reviewed-by: "+verdicts[rid])
	}
	out.Node, out.Trailers = id, strings.Join(lines, "\n")
	if n.State != "done" {
		n.State = "done"
		n.Revision = w.PlanRevision + 1
		w.PlanRevision++
	}
	PlanNudges(w)
	return out, nil
}

// ActiveNodes lists nodes still active or reported, for the end-of-workspace
// check.
func ActiveNodes(w *Workspace) []string {
	var out []string
	for _, n := range w.Plan {
		if n.State == "active" || n.State == "reported" {
			out = append(out, n.ID)
		}
	}
	return out
}
