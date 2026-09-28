package agentd

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

// The supervisor tells the orchestrator when work has stalled. Every other
// nudge fires on an event (a turn ended, a member ended, a milestone done);
// nothing noticed that time passed and nothing happened. It is plain code on
// the workspace loop, reading what Wash already holds; it never prompts a
// member, and it spends a turn only when it has something to say.
//
// A member is wedged when it is in a turn and nothing has come from it for
// quiet: no output, no open tool, no question waiting, no busy process.
// That is reported as soon as it is seen. The rest waits until the whole
// team has been idle for idle: open work nobody is doing, ready nodes with
// nobody on them, reported nodes not accepted, and who is waiting on what.
// The same findings are sent again after repeat, doubling, and after
// max_prompts the owner is told instead.

const (
	superviseEvery    = 5 * time.Second
	defaultQuiet      = 2 * time.Minute
	defaultIdle       = time.Minute
	defaultRepeat     = 5 * time.Minute
	defaultMaxPrompts = 3
	// busyTicks is the CPU time (clock ticks, 1/100 s) an adapter's process
	// tree must use between checks to count as busy. Idle Claude adapters
	// measured 1–4 per 5 s; a build or a streaming model hundreds.
	busyTicks = 20
)

type supervisor struct {
	last time.Time
	// cpu is each session's process-tree CPU time at the last check.
	cpu map[string]uint64
	// idleSince is when a member was first seen idle with open work; stall
	// when the whole team of a workspace was first seen idle.
	idleSince map[string]time.Time
	stall     map[string]time.Time
	// sent is what each workspace has been told, by finding key.
	sent map[string]map[string]*sentFinding
}

type sentFinding struct {
	at    time.Time
	times int
}

type finding struct {
	key, text string
	// owner findings go to the owner, not the orchestrator (the
	// orchestrator itself is wedged).
	owner bool
}

func newSupervisor() *supervisor {
	return &supervisor{cpu: map[string]uint64{}, idleSince: map[string]time.Time{}, stall: map[string]time.Time{}, sent: map[string]map[string]*sentFinding{}}
}

// memberRuntime is what the supervisor reads of a member's session.
type memberRuntime struct {
	live                     bool // a session is up
	busy, tool, asks, bgWork bool
	cpuBusy                  bool
	heard                    time.Time
}

// readProcs lists every process as pid → (parent, CPU ticks). A variable
// for tests.
var readProcs = func() map[int][2]uint64 {
	out := map[int][2]uint64{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command name is in parentheses and may hold spaces.
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 || i+2 > len(s) {
			continue
		}
		f := strings.Fields(s[i+2:])
		if len(f) < 13 {
			continue
		}
		ppid, _ := strconv.ParseUint(f[1], 10, 64)
		utime, _ := strconv.ParseUint(f[11], 10, 64)
		stime, _ := strconv.ParseUint(f[12], 10, 64)
		out[pid] = [2]uint64{ppid, utime + stime}
	}
	return out
}

// treeTicks is the CPU time of root and everything under it.
func treeTicks(procs map[int][2]uint64, root int) uint64 {
	kids := map[int][]int{}
	for pid, p := range procs {
		kids[int(p[0])] = append(kids[int(p[0])], pid)
	}
	var total uint64
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		total += procs[queue[0]][1]
		queue = append(queue, kids[queue[0]]...)
	}
	return total
}

func (s *supervisor) runtime(m swarm.Member, h *hosted, procs map[int][2]uint64) memberRuntime {
	if h == nil || h.closing.Load() || !h.sessionReady.Load() {
		return memberRuntime{}
	}
	r := memberRuntime{live: true, heard: time.UnixMilli(h.heard.Load()), bgWork: h.background() != ""}
	h.turnMu.Lock()
	r.busy, r.tool, r.asks = h.busy(), len(h.activityTools) > 0, h.activityAsks > 0
	h.turnMu.Unlock()
	if h.pid > 0 && procs != nil {
		ticks := treeTicks(procs, h.pid)
		if prev, ok := s.cpu[m.Session]; ok && ticks >= prev+busyTicks {
			r.cpuBusy = true
		}
		s.cpu[m.Session] = ticks
	}
	return r
}

func supervisorSettings(w *swarm.Workspace) (quiet, idle, repeat time.Duration, maxPrompts int) {
	parse := func(v string, d time.Duration) time.Duration {
		if p, err := time.ParseDuration(v); err == nil && v != "" {
			return p
		}
		return d
	}
	c := w.Supervisor
	maxPrompts = c.MaxPrompts
	if maxPrompts == 0 {
		maxPrompts = defaultMaxPrompts
	}
	return parse(c.Quiet, defaultQuiet), parse(c.Idle, defaultIdle), parse(c.Repeat, defaultRepeat), maxPrompts
}

// supervise runs from the workspace loop; it checks at most every
// superviseEvery.
func (ws *workspaceService) supervise(now time.Time) {
	if ws.sup == nil {
		ws.sup = newSupervisor()
	}
	s := ws.sup
	if now.Sub(s.last) < superviseEvery {
		return
	}
	s.last = now
	procs := readProcs()
	for _, w := range ws.store.Snapshot().Workspaces {
		if w.State != "active" || w.Supervisor.Off {
			delete(s.sent, w.ID)
			delete(s.stall, w.ID)
			continue
		}
		runtime := map[string]memberRuntime{}
		for _, m := range w.Members {
			if m.State != "ended" {
				runtime[m.ID] = s.runtime(m, workspaceHosted(m.Session), procs)
			}
		}
		ws.report(&w, s.findings(&w, runtime, now), now)
	}
}

// findings is what is wrong in w now.
func (s *supervisor) findings(w *swarm.Workspace, runtime map[string]memberRuntime, now time.Time) []finding {
	quiet, idle, _, _ := supervisorSettings(w)
	var out []finding
	name := func(id string) string {
		if m := swarm.GetMember(w, id); m != nil {
			return m.Name + " (" + memberRef(*m) + ")"
		}
		return id
	}
	queued := map[string]int{}
	ownerWait := false
	for _, msg := range w.Messages {
		if msg.State == "queued" {
			queued[msg.Recipient]++
		}
		if msg.Type == "decision_request" && msg.State == "recorded" {
			ownerWait = true
		}
	}
	activeWorkers := 0
	for _, m := range w.Members {
		if r := runtime[m.ID]; m.ID != w.Lead && r.busy {
			activeWorkers++
		}
	}

	// Wedged: in a turn, and nothing from it for quiet.
	teamBusy := false
	for _, m := range w.Members {
		r := runtime[m.ID]
		if m.State == "ended" || !r.live {
			continue
		}
		working := r.tool || r.asks || r.cpuBusy || now.Sub(r.heard) < quiet
		if r.busy && !working {
			silent := now.Sub(r.heard).Round(time.Second)
			if m.ID == w.Lead {
				out = append(out, finding{key: "wedged:" + m.ID, owner: true, text: fmt.Sprintf("The orchestrator has been in a turn for %s with nothing from it. Interrupt it from its window, or end it.", silent)})
				continue
			}
			text := fmt.Sprintf("wedged: %s has been in a turn for %s with nothing from it (no output, no tool, no busy process)", name(m.ID), silent)
			if n := queued[m.ID]; n > 0 {
				text += fmt.Sprintf("; %d message(s) queued for it", n)
			}
			out = append(out, finding{key: "wedged:" + m.ID, text: text + ". Interrupt it (member_control interrupt); if that does not free it, end it and relaunch with handoff_file."})
			continue
		}
		if r.busy || r.bgWork || r.asks || r.cpuBusy {
			teamBusy = true
		}
	}

	// Stopped: open work on a member that cannot do it.
	for _, a := range w.Assignments {
		if !a.Open() {
			continue
		}
		m := swarm.GetMember(w, a.Member)
		switch {
		case m == nil:
		case m.State == "paused" || m.State == "failed" || m.State == "ended":
			out = append(out, finding{key: "stopped:" + a.ID + ":" + m.State, text: fmt.Sprintf("stopped: %s is %s with assignment %s%s open. Resume it, or reassign the work.", name(m.ID), m.State, a.ID, onNode(a.Node))})
		case m.State == "available" && !runtime[m.ID].live:
			out = append(out, finding{key: "stopped:" + a.ID + ":offline", text: fmt.Sprintf("stopped: %s has no running session and assignment %s%s open. Resume it (member_control resume), or reassign the work.", name(m.ID), a.ID, onNode(a.Node))})
		}
	}

	// Idle with work: not busy, open assignment, no report, not waiting.
	for _, m := range w.Members {
		r := runtime[m.ID]
		if m.ID == w.Lead || m.State != "available" || !r.live || r.busy || r.bgWork || r.asks || r.cpuBusy || m.Waiting != "" {
			delete(s.idleSince, m.Session)
			continue
		}
		var open *swarm.Assignment
		for i := range w.Assignments {
			if a := &w.Assignments[i]; a.Member == m.ID && a.State == "active" {
				open = a
				break
			}
		}
		if open == nil {
			delete(s.idleSince, m.Session)
			continue
		}
		since, ok := s.idleSince[m.Session]
		if !ok {
			s.idleSince[m.Session] = now
			continue
		}
		if now.Sub(since) >= idle {
			out = append(out, finding{key: "idle:" + m.ID + ":" + open.ID, text: fmt.Sprintf("idle with work: %s has been idle for %s with assignment %s%s open, no report and no waiting set. Tell it to continue or report, or reassign.", name(m.ID), now.Sub(since).Round(time.Second), open.ID, onNode(open.Node))})
		}
	}

	// Mail that is not going out to a member that is free to take it.
	for _, m := range w.Members {
		r := runtime[m.ID]
		if m.ID == w.Lead || m.State != "available" || !r.live || r.busy || queued[m.ID] == 0 || activeWorkers >= w.MaxActive {
			continue
		}
		oldest := now
		for _, msg := range w.Messages {
			if msg.Recipient == m.ID && msg.State == "queued" && time.UnixMilli(msg.Created).Before(oldest) {
				oldest = time.UnixMilli(msg.Created)
			}
		}
		if now.Sub(oldest) >= quiet {
			out = append(out, finding{key: "queued:" + m.ID, text: fmt.Sprintf("undelivered: %d message(s) for %s have been queued for %s and it is not in a turn. Its session may be stuck: interrupt it, or end it and relaunch.", queued[m.ID], name(m.ID), now.Sub(oldest).Round(time.Second))})
		}
	}

	// The whole team idle: what is left to do, and who waits on what.
	if teamBusy || ownerWait {
		delete(s.stall, w.ID)
		return out
	}
	since, ok := s.stall[w.ID]
	if !ok {
		s.stall[w.ID] = now
		return out
	}
	if now.Sub(since) < idle {
		return out
	}
	unfinished := false
	for _, n := range w.Plan {
		if n.Template == "note" || n.State == "done" {
			continue
		}
		unfinished = true
		leaf := !slices.ContainsFunc(w.Plan, func(c swarm.Node) bool { return c.Parent == n.ID })
		open := len(swarm.OpenOn(w, n.ID)) > 0
		switch {
		case n.State == "reported":
			out = append(out, finding{key: "reported:" + n.ID, text: fmt.Sprintf("reported: node %s (%s) is reported. Accept it (plan_accept) or reopen it.", n.ID, n.Title)})
		case n.State == "failed" && !open:
			out = append(out, finding{key: "failed:" + n.ID, text: fmt.Sprintf("failed: node %s (%s) failed and nobody is on it.", n.ID, n.Title)})
		case leaf && n.Template != "milestone" && n.State == "todo" && !open && len(swarm.Unmet(w, n.ID)) == 0:
			out = append(out, finding{key: "ready:" + n.ID, text: fmt.Sprintf("ready: node %s (%s) has its needs met and nobody on it.", n.ID, n.Title)})
		}
	}
	var waiting []string
	for _, m := range w.Members {
		if m.ID != w.Lead && m.State == "available" && m.Waiting != "" {
			waiting = append(waiting, fmt.Sprintf("%s waits: %q", name(m.ID), m.Waiting))
		}
	}
	if len(waiting) > 0 && unfinished {
		slices.Sort(waiting)
		out = append(out, finding{key: "waiting:" + strings.Join(waiting, "|"), text: "waiting: the team has been idle for " + now.Sub(since).Round(time.Second).String() + " with the plan unfinished. " + strings.Join(waiting, "; ") + ". Check that what each waits for is coming."})
	}
	return out
}

func onNode(node string) string {
	if node == "" {
		return ""
	}
	return " (node " + node + ")"
}

// report sends the orchestrator the findings, when one is new or its repeat
// is due, and tells the owner when the orchestrator has been told enough.
func (ws *workspaceService) report(w *swarm.Workspace, found []finding, now time.Time) {
	s := ws.sup
	_, _, repeat, maxPrompts := supervisorSettings(w)
	sent := s.sent[w.ID]
	if sent == nil {
		sent = map[string]*sentFinding{}
		s.sent[w.ID] = sent
	}
	current := map[string]bool{}
	var due, owner []finding
	for _, f := range found {
		current[f.key] = true
		p := sent[f.key]
		if p != nil && now.Sub(p.at) < repeat<<min(p.times-1, 10) {
			continue
		}
		if p == nil {
			p = &sentFinding{}
			sent[f.key] = p
		}
		p.at = now
		p.times++
		switch {
		case f.owner && p.times <= maxPrompts:
			owner = append(owner, f)
		case f.owner:
		case p.times <= maxPrompts:
			due = append(due, f)
		case p.times == maxPrompts+1:
			owner = append(owner, f)
		}
	}
	// A finding that went away is forgotten, so its return is news.
	for key := range sent {
		if !current[key] {
			delete(sent, key)
		}
	}
	lead := workspaceLeadSession(*w)
	if len(due) > 0 {
		var b strings.Builder
		b.WriteString("Supervisor: work has stalled.\n")
		for _, f := range found {
			if !f.owner {
				b.WriteString("\n- " + f.text)
			}
		}
		fmt.Fprintf(&b, "\n\nThis repeats while nothing changes (the next in %s).", repeat)
		err := ws.store.Mutate(lead, false, func(w *swarm.Workspace, _ *swarm.Member) error {
			_, err := swarm.AddMessage(w, "wash", w.Lead, "lifecycle", b.String(), "", "", "")
			return err
		})
		if err != nil {
			log.Printf("agentd: supervisor workspace=%s: %v", w.ID, err)
		} else {
			log.Printf("agentd: supervisor workspace=%s prompted the orchestrator: %d finding(s)", w.ID, len(found))
			ws.signal()
		}
	}
	if len(owner) > 0 {
		var texts []string
		for _, f := range owner {
			texts = append(texts, f.text)
		}
		body := strings.Join(texts, "\n")
		if !slices.ContainsFunc(owner, func(f finding) bool { return f.owner }) {
			body = fmt.Sprintf("The orchestrator has been told %d times and nothing has changed:\n%s", maxPrompts, body)
		}
		_ = ws.store.Mutate(lead, false, func(w *swarm.Workspace, _ *swarm.Member) error {
			_, err := swarm.AddMessage(w, "wash", "human", "flash", "⏳ "+body, "", "", "")
			return err
		})
		if ws.conn != nil {
			key := ""
			if h := workspaceHosted(lead); h != nil {
				key = h.key
			}
			desktop(ws.conn, agentproto.Notify{Key: key, Title: w.Name + " · supervisor", Body: body, Level: "warn"})
		}
	}
}
