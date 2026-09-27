package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/swarm"
)

func decodeWorkspace(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

type memberSpec struct {
	Capability string `json:"capability,omitempty"`
	Approval   string `json:"approval,omitempty"`
	Name       string `json:"name"`
	// Catalog names another catalog than the workspace's for this member;
	// Model is a slot of that catalog (frontier, coding, small) or a model
	// id its adapter offers. Empty is the catalog's default.
	Catalog      string            `json:"catalog,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	Configs      map[string]string `json:"configs,omitempty"`
	Subagents    string            `json:"subagents,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`
	Instructions string            `json:"instructions"`
	Lifetime     string            `json:"lifetime"`
	Task         string            `json:"task,omitempty"`
	CanSpawn     bool              `json:"can_spawn,omitempty"`
	// Node is the plan node the member works on; none is the team.
	Node string `json:"node,omitempty"`
	Role string `json:"role,omitempty"`
}
type bulkConfig struct {
	swarm.ConfigurePatch
	Workspace *struct {
		Name string `json:"name"`
		Root string `json:"project_root"`
	} `json:"workspace,omitempty"`
	Members  map[string]*memberSpec `json:"members,omitempty"`
	QADir    json.RawMessage        `json:"qa_dir,omitempty"`
	PlanFile json.RawMessage        `json:"plan_file,omitempty"`
	Request  string                 `json:"request_id,omitempty"`
	Preview  bool                   `json:"preview,omitempty"`
}

func (ws *workspaceService) configureBulk(ctx context.Context, h *hosted, raw json.RawMessage) (any, error) {
	var p bulkConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		if string(value) == "null" && key != "plan_file" && key != "qa_dir" {
			return nil, fmt.Errorf("%s cannot be null", key)
		}
	}
	if err := decodeWorkspace(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Members) > 64 {
		return nil, errors.New("maximum 64 member entries")
	}
	hostedMu.Lock()
	callerAuto := h.yolo
	hostedMu.Unlock()
	// Validate paths before committing. No process starts during validation/preview.
	root := h.cwd
	approvedRoot := ""
	if existing := ws.store.View(h.sessionID); existing != nil {
		root = existing.Root
		approvedRoot = existing.Root
	}
	if p.Workspace != nil && p.Workspace.Root != "" {
		root = p.Workspace.Root
	}
	// A path inside the project root asks nobody: the root is the one folder
	// question, answered (or inside the session already) when the workspace
	// was set up. Asking again for its plan, its QA file and every member's
	// worktree put the orchestrator in front of the human on each configure
	// call with a "Read … (outside this session's folders)" that could only
	// be answered yes.
	confine := func(tool, path string) (string, error) {
		if approvedRoot != "" {
			if abs, err := wfs.New(approvedRoot).Confine(path); err == nil {
				return abs, nil
			}
		}
		return h.confineOrAsk(ctx, tool, path)
	}
	if ws.store.View(h.sessionID) == nil || p.Workspace != nil {
		var err error
		root, err = confine("Read", root)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("project_root must be a directory")
		}
	}
	approvedRoot = root
	// plan_file: where Wash writes the plan as it changes; null stops it.
	planFile, planDetach := "", false
	var planResume []swarm.Node
	planLegend := ""
	if len(p.PlanFile) > 0 {
		if string(p.PlanFile) == "null" {
			planDetach = true
		} else {
			if err := json.Unmarshal(p.PlanFile, &planFile); err != nil || !swarm.ValidText(planFile, 4096) {
				return nil, errors.New("plan_file must be a file path")
			}
			if !filepath.IsAbs(planFile) {
				planFile = filepath.Join(root, planFile)
			}
			path, err := confine("Write", planFile)
			if err != nil {
				return nil, err
			}
			planFile = filepath.Clean(path)
			if !strings.EqualFold(filepath.Ext(planFile), ".toml") {
				return nil, errors.New("plan_file must be a .toml file")
			}
			if err := checkPlanFileTarget(planFile); err != nil {
				return nil, err
			}
			// A plan file Wash wrote earlier is this project's plan: a
			// workspace set up on it resumes it, as qa_dir resumes threads.
			if info, err := os.Stat(planFile); err == nil && info.Size() > 0 {
				nodes, legend, err := readPlanFile(planFile)
				if err != nil {
					return nil, err
				}
				planResume, planLegend = nodes, legend
			}
		}
	}
	// qa_dir: a string sets the QA directory, null detaches it. The
	// directory holds one file per thread; one that does not exist yet is
	// created when the configuration commits.
	qaDir, qaDetach := "", false
	var qaRestore []swarm.QAFile
	qaLocked := false
	defer func() {
		if qaLocked {
			ws.qaMu.Unlock()
		}
	}()
	if len(p.QADir) > 0 {
		if string(p.QADir) == "null" {
			qaDetach = true
		} else {
			if err := json.Unmarshal(p.QADir, &qaDir); err != nil || !swarm.ValidText(qaDir, 4096) {
				return nil, errors.New("qa_dir must be a directory path")
			}
			if !filepath.IsAbs(qaDir) {
				qaDir = filepath.Join(root, qaDir)
			}
			path, err := confine("Write", qaDir)
			if err != nil {
				return nil, err
			}
			qaDir = filepath.Clean(path)
			if info, err := os.Lstat(qaDir); err == nil {
				if !info.IsDir() {
					return nil, errors.New("qa_dir must be a directory")
				}
				if qaDir, err = filepath.EvalSymlinks(qaDir); err != nil {
					return nil, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	keys := make([]string, 0, len(p.Members))
	for key, m := range p.Members {
		if !swarm.ValidProfileName(key) || slices.Contains([]string{"conversation", "plan", "qa", swarm.OrchestratorKey}, key) || m == nil {
			return nil, errors.New("invalid member key; use member_control to end members")
		}
		// Each check names the member and the field: a bare "invalid member
		// definition" left the orchestrator diffing its call by eye.
		switch {
		case !swarm.ValidText(m.Name, 120):
			return nil, fmt.Errorf("member %s: name is required (at most 120 bytes)", key)
		case !swarm.ValidText(m.Instructions, 30000):
			return nil, fmt.Errorf("member %s: instructions are required (at most 30000 bytes)", key)
		case !slices.Contains([]string{"resident", "ephemeral"}, m.Lifetime):
			return nil, fmt.Errorf(`member %s: lifetime must be "resident" or "ephemeral"`, key)
		case m.Lifetime == "ephemeral" && !swarm.ValidText(m.Task, 32768):
			return nil, fmt.Errorf("member %s: an ephemeral member needs a task", key)
		case len(m.Task) > 32768:
			return nil, fmt.Errorf("member %s: task exceeds 32 KiB", key)
		}
		if len(memberBrief(swarm.Member{ID: swarm.ID(), Instructions: m.Instructions, InitialTask: m.Task}, swarm.ID())) > 32768 {
			return nil, fmt.Errorf("member %s: instructions and task together exceed 32 KiB: a member receives them as one first message; put detail in a file it can read", key)
		}
		if m.Node != "" && !swarm.ValidProfileName(m.Node) {
			return nil, fmt.Errorf("member %s: node must be a plan node id", key)
		}
		if !slices.Contains([]string{"", "architect", "implementer", "reviewer"}, m.Role) {
			return nil, fmt.Errorf("member %s: role must be architect, implementer or reviewer", key)
		}
		// A member works in the project, not wherever the orchestrator
		// happens to run: defaulting to the caller's folder launched every
		// member of a /tmp/tally-p5 workspace in the orchestrator's own
		// repository, where an auto-approved implementer edits the wrong
		// tree. A relative cwd is inside the project too.
		if m.Cwd == "" {
			m.Cwd = root
		} else if !filepath.IsAbs(m.Cwd) {
			m.Cwd = filepath.Join(root, m.Cwd)
		}
		cwd, err := confine("Read", m.Cwd)
		if err != nil {
			return nil, fmt.Errorf("member %s: cwd: %w", key, err)
		}
		m.Cwd = cwd
		info, err := os.Stat(cwd)
		if err != nil {
			return nil, fmt.Errorf("member %s: cwd: %w", key, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("member %s: cwd must be a directory", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Catalogs and connections are read once per call: a member's model
	// resolves against what agents.json says now, and is then a snapshot.
	pol := hostedPolicy()
	catalogs, badCatalogs := loadCatalogs(pol)
	if p.Catalog != nil && *p.Catalog != "" {
		if _, ok := catalogs[*p.Catalog]; !ok {
			return nil, fmt.Errorf("unknown catalog %q", *p.Catalog)
		}
		if err := badCatalogs[*p.Catalog]; err != nil {
			return nil, err
		}
	}
	// Never hold the projection lock while path approval can wait on a human.
	// Read/claim under the same lock as export to avoid importing a stale write.
	if qaDir != "" {
		ws.qaMu.Lock()
		qaLocked = true
		var err error
		if qaRestore, err = readQADir(qaDir); err != nil {
			return nil, err
		}
	}
	result, err := ws.store.Transaction(h.sessionID, "workspace_configure", p.Request, raw, p.Preview, func(s *swarm.Store) (any, error) {
		current := s.View(h.sessionID)
		if current == nil {
			if p.Expected != nil && *p.Expected != 0 {
				return nil, errors.New("new workspace requires expected_revision 0 or omitted")
			}
			if p.Workspace == nil || p.Workspace.Name == "" {
				return nil, errors.New("initial configuration requires workspace.name")
			}
			if _, err := s.Setup(h.sessionID, h.agent, h.cwd, p.Workspace.Name, root); err != nil {
				return nil, err
			}
			// Resuming the orchestrator reads its launch settings like any
			// member's, and must come back through the connection it runs on.
			// Members' models come from the orchestrator's own catalog unless
			// the call names another.
			if err := s.Mutate(h.sessionID, true, func(w *swarm.Workspace, lead *swarm.Member) error {
				lead.LaunchSettings = &swarm.AgentProfile{Provider: h.agent, Connection: h.connection}
				lead.Catalog, lead.Model = h.catalog, h.model
				w.Catalog = h.catalog
				if w.Catalog == "" {
					w.Catalog = autoCatalogFor(catalogs, h.agent, h.connection)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		} else {
			if p.Expected != nil && *p.Expected != current.Revision {
				return nil, errors.New("workspace revision conflict; read current state")
			}
			if p.Workspace != nil && root != current.Root {
				return nil, errors.New("project_root is immutable; end the workspace explicitly")
			}
		}
		patch := p.ConfigurePatch
		patch.Expected = nil
		if p.Workspace != nil {
			patch.Name = &p.Workspace.Name
		}
		if _, err := s.Configure(h.sessionID, patch); err != nil {
			return nil, err
		}
		allWorkspaces := s.Snapshot().Workspaces
		err := s.Mutate(h.sessionID, true, func(w *swarm.Workspace, creator *swarm.Member) error {
			if qaDetach {
				w.QADir = ""
			}
			if qaDir != "" && w.QADir != qaDir {
				for _, other := range allWorkspaces {
					if other.ID != w.ID && other.State != "ended" && other.QADir == qaDir {
						return fmt.Errorf("QA directory is used by workspace %s (%q); if its orchestrator is not running, end it with workspace_end {\"workspace_id\":%q}", other.ID, other.Name, other.ID)
					}
				}
				if len(qaRestore) > 0 {
					if err := restoreQA(w, qaRestore); err != nil {
						return err
					}
				}
				w.QADir = qaDir
			}
			if planDetach {
				w.PlanFile = ""
			}
			if planFile != "" {
				for _, other := range allWorkspaces {
					if other.ID != w.ID && other.State != "ended" && other.PlanFile == planFile {
						return fmt.Errorf("plan file is used by workspace %s (%q)", other.ID, other.Name)
					}
				}
				if w.PlanFile != planFile && len(planResume) > 0 {
					if len(w.Plan) > 0 {
						return fmt.Errorf("%s holds a plan and this workspace has its own; load that one with plan_set {\"from\": …}, or choose another plan_file", filepath.Base(planFile))
					}
					if err := swarm.ReplacePlan(w, creator, planResume); err != nil {
						return fmt.Errorf("%s: %w", filepath.Base(planFile), err)
					}
					if w.Legend == "" {
						w.Legend = planLegend
					}
				}
				w.PlanFile = planFile
			}
			for _, key := range keys {
				spec := p.Members[key]
				// A launch that failed never ran: nothing holds its session,
				// its assignments or its name. Its key takes a corrected
				// definition in place (same ID), so one wrong model string
				// costs one call rather than an end plus new keys.
				redefine := swarm.GetMember(w, key)
				if redefine != nil && !neverLaunched(redefine) {
					redefine = nil
				}
				if prior := swarm.GetMember(w, key); prior != nil && redefine == nil {
					if prior.State == "ended" {
						return fmt.Errorf("member %s is ended; use a new key for replacement", key)
					}
					if prior.Name != spec.Name || spec.Catalog != "" && prior.Catalog != spec.Catalog || spec.Model != "" && prior.Model != spec.Model || prior.Cwd != spec.Cwd || prior.Instructions != spec.Instructions || prior.Lifetime != spec.Lifetime || prior.CanSpawn != spec.CanSpawn || prior.Node != spec.Node || prior.Role != spec.Role || prior.InitialTask != spec.Task {
						return fmt.Errorf("member %s already exists with different settings; end and replace explicitly", key)
					}
					// Catalog edits affect future launches; explicit launch overrides must still match.
					old := prior.LaunchSettings
					if spec.Capability != "" && old.Capability != spec.Capability || spec.Approval != "" && old.Approval != spec.Approval || spec.Provider != "" && old.Provider != spec.Provider || spec.Effort != "" && old.Effort != spec.Effort || spec.Subagents != "" && old.Subagents != spec.Subagents {
						return fmt.Errorf("member %s already exists with different launch settings; end and replace explicitly", key)
					}
					for id, val := range spec.Configs {
						if old.Configs[id] != val {
							return fmt.Errorf("member %s already exists with different adapter settings; end and replace explicitly", key)
						}
					}
					continue
				}
				live := 0
				for _, m := range w.Members {
					if m.State == "ended" || neverLaunched(&m) {
						continue
					}
					live++
					// Unique within a package, not the workspace: with titled
					// packages a member's name is its role, so every package
					// has an "Implementer". Lookup is by ID or key, never name.
					if m.Name == spec.Name && m.Node == spec.Node {
						return fmt.Errorf("member %s: name %q is already taken on this node", key, spec.Name)
					}
				}
				// A member works on a plan node, and its task is an
				// assignment there, so the node must exist and be ready.
				if spec.Node != "" && swarm.PlanNode(w, spec.Node) == nil {
					return fmt.Errorf("member %s: node %q is not in the plan; add it with plan_set first", key, spec.Node)
				}
				if spec.Task != "" {
					if spec.Node == "" {
						return fmt.Errorf("member %s: a task is an assignment on a plan node; give the member a node", key)
					}
					if unmet := swarm.Unmet(w, spec.Node); len(unmet) > 0 {
						return fmt.Errorf("member %s: node %s needs %s first; launch it without a task and assign with override", key, spec.Node, strings.Join(unmet, ", "))
					}
				}
				if live >= w.MaxMembers {
					return fmt.Errorf("member %s: workspace member limit (%d) reached", key, w.MaxMembers)
				}
				explicit := swarm.AgentProfile{Capability: spec.Capability, Approval: spec.Approval, Provider: spec.Provider, Effort: spec.Effort, Configs: spec.Configs, Subagents: spec.Subagents}
				catalog, settings, err := memberSettingsFor(w, catalogs, badCatalogs, spec.Catalog, spec.Model, explicit)
				if err != nil {
					return fmt.Errorf("member %s: %w", key, err)
				}
				// Children stay within the launcher's authority: only a session
				// that is itself auto-approved can launch one that is, so no agent
				// grants a teammate what the human has not granted it.
				if settings.Approval == "auto" && !callerAuto {
					return fmt.Errorf(`member %s: approval "auto" requires the configuring session to be auto-approved itself`, key)
				}
				if settings.Capability == "reviewer" && spec.CanSpawn {
					return fmt.Errorf("member %s: reviewer capability cannot spawn agents", key)
				}
				if err := knownProvider(settings.Provider); err != nil {
					return fmt.Errorf("member %s: %w", key, err)
				}
				member := swarm.Member{ID: swarm.ID(), Key: key, Name: spec.Name, Catalog: catalog, Model: spec.Model, Provider: settings.Provider, LaunchSettings: &settings, Cwd: spec.Cwd, Instructions: spec.Instructions, InitialTask: spec.Task, Lifetime: spec.Lifetime, State: "pending", Creator: creator.ID, CanSpawn: spec.CanSpawn, Node: spec.Node, Role: spec.Role}
				if redefine != nil {
					member.ID = redefine.ID
					*redefine = member
					continue
				}
				w.Members = append(w.Members, member)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := s.ClaimFiles(h.sessionID); err != nil {
			return nil, err
		}
		w := s.View(h.sessionID)
		members := map[string]string{}
		for _, key := range keys {
			members[key] = swarm.GetMember(w, key).ID
		}
		// A preview's new ids are made up for the staged copy and thrown
		// away with it; returned, they read as ids the commit would keep.
		// Only ids that already exist are reported: new members are
		// addressed by key.
		workspaceID := w.ID
		if p.Preview {
			if current == nil {
				workspaceID = ""
			}
			for key := range members {
				if current == nil || swarm.GetMember(current, key) == nil {
					members[key] = ""
				}
			}
		}
		return map[string]any{"workspace_id": workspaceID, "revision": w.Revision, "members": members, "preview": p.Preview, "configuration": map[string]any{"name": w.Name, "project_root": w.Root, "catalog": w.Catalog, "qa_dir": w.QADir, "plan_file": w.PlanFile, "legend": w.Legend, "max_active": w.MaxActive, "max_members": w.MaxMembers}}, nil
	})
	if qaLocked {
		ws.qaMu.Unlock()
		qaLocked = false
	}
	if err != nil || p.Preview {
		return result, err
	}
	if qaDir != "" {
		if err := os.MkdirAll(qaDir, 0o755); err != nil {
			return nil, fmt.Errorf("configured, but the QA directory could not be created: %w", err)
		}
	}
	// The durable configuration is complete. Launch outcomes are independent and
	// retries never launch available/starting members again.
	var receipt struct {
		WorkspaceID string            `json:"workspace_id"`
		Members     map[string]string `json:"members"`
	}
	encoded, _ := json.Marshal(result)
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		return nil, err
	}
	// Launches run one after another (npx launches queue anyway) and can
	// outlast the bridge's 90s request timeout. Tied to the request, the
	// client's disconnect cancelled every later launch mid-setup. They run
	// detached; the call reports what finished within launchWait, and a
	// launch that fails after that tells the orchestrator itself.
	var mu sync.Mutex
	outcomes := map[string]any{}
	replied := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		launchCtx := context.WithoutCancel(ctx)
		for _, key := range keys {
			outcome := ws.launchOutcome(launchCtx, h, receipt.WorkspaceID, receipt.Members[key], key)
			mu.Lock()
			late := replied
			if outcome != nil && !late {
				outcomes[key] = outcome
			}
			mu.Unlock()
			if failed, ok := outcome.(map[string]string); late && ok && failed["error"] != "" {
				_ = ws.store.Mutate(h.sessionID, false, func(w *swarm.Workspace, _ *swarm.Member) error {
					_, err := swarm.AddMessage(w, receipt.Members[key], w.Lead, "lifecycle", key+" failed to launch: "+failed["error"]+". Resume it with member_control.", "", "", "")
					return err
				})
				ws.signal()
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(launchWait):
	}
	mu.Lock()
	defer mu.Unlock()
	replied = true
	for _, key := range keys {
		if _, ok := outcomes[key]; !ok {
			outcomes[key] = map[string]string{"state": "starting", "note": "still launching; a failure will be sent to you"}
		}
	}
	return map[string]any{"receipt": result, "launches": outcomes}, nil
}

// launchWait bounds how long workspace_configure waits for its launches,
// inside the bridge's 90s request timeout.
var launchWait = 60 * time.Second

// launchOutcome launches a pending member and reports its state, or nil when
// it no longer exists.
func (ws *workspaceService) launchOutcome(ctx context.Context, h *hosted, workspaceID, id, key string) any {
	w := ws.store.View(h.sessionID)
	if w == nil || w.ID != workspaceID {
		return map[string]string{"state": "ended"}
	}
	m := swarm.GetMember(w, id)
	if m == nil {
		return nil
	}
	if m.State == "pending" {
		if _, err := ws.spawn(ctx, h, m.ID); err != nil {
			state := "ended"
			if current := swarm.GetMember(ws.store.View(h.sessionID), m.ID); current != nil {
				state = current.State
			}
			return map[string]string{"state": state, "error": err.Error()}
		}
	}
	if current := ws.store.View(h.sessionID); current != nil {
		return swarm.GetMember(current, key)
	}
	return nil
}

// neverLaunched is a member whose launch failed before it had a session.
func neverLaunched(m *swarm.Member) bool { return m.State == "failed" && m.Session == "" }

func knownProvider(provider string) error {
	for _, a := range adapters {
		if a.ID == provider {
			return nil
		}
	}
	return fmt.Errorf("unknown provider %q", provider)
}

// memberSettingsFor is a new member's launch settings: the model it asked
// for, a slot of its catalog (the workspace's unless it named one) or a
// model id on that catalog's adapter, with its explicit settings on top.
// Resolved now and kept: the member's instructions never carry a model
// name, and a later catalog change moves no running member.
func memberSettingsFor(w *swarm.Workspace, catalogs map[string]Catalog, bad map[string]error, catalog, model string, explicit swarm.AgentProfile) (string, swarm.AgentProfile, error) {
	if catalog == "" {
		catalog = w.Catalog
	}
	if catalog == "" {
		return "", swarm.AgentProfile{}, errors.New(`the workspace has no catalog; set one with workspace_configure {"catalog":…}`)
	}
	base, err := resolveCatalog(catalogs, bad, catalog, model)
	if err != nil {
		return "", swarm.AgentProfile{}, err
	}
	settings, err := swarm.Overlay(base, explicit)
	return catalog, settings, err
}
