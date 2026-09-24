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
	Capability   string            `json:"capability,omitempty"`
	Approval     string            `json:"approval,omitempty"`
	Name         string            `json:"name"`
	Profile      string            `json:"profile,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	Thinking     string            `json:"thinking,omitempty"`
	Configs      map[string]string `json:"configs,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`
	Instructions string            `json:"instructions"`
	Lifetime     string            `json:"lifetime"`
	Task         string            `json:"task,omitempty"`
	CanSpawn     bool              `json:"can_spawn,omitempty"`
	Package      string            `json:"package,omitempty"`
	Role         string            `json:"role,omitempty"`
}
type planPatch struct {
	Items map[string]json.RawMessage `json:"items"`
	Order []string                   `json:"order,omitempty"`
}
type bulkConfig struct {
	swarm.ConfigurePatch
	Workspace *struct {
		Name string `json:"name"`
		Root string `json:"project_root"`
	} `json:"workspace,omitempty"`
	Members    map[string]*memberSpec `json:"members,omitempty"`
	Plan       *planPatch             `json:"plan,omitempty"`
	QADocument json.RawMessage        `json:"qa_document,omitempty"`
	Document   json.RawMessage        `json:"document,omitempty"`
	Request    string                 `json:"request_id,omitempty"`
	Preview    bool                   `json:"preview,omitempty"`
}

func (ws *workspaceService) configureBulk(ctx context.Context, h *hosted, raw json.RawMessage) (any, error) {
	var p bulkConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		if string(value) == "null" && key != "document" && key != "qa_document" {
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
	var doc *swarm.Document
	if len(p.Document) > 0 && string(p.Document) != "null" {
		if err := decodeWorkspace(p.Document, &doc); err != nil {
			return nil, err
		}
		path, err := confine("Read", doc.Path)
		if err != nil {
			return nil, err
		}
		if _, err = readWorkspaceDocument(path); err != nil {
			return nil, err
		}
		doc.Path = path
	}
	var qaDoc *swarm.Document
	var qaRestore *qaArchive
	qaLocked := false
	defer func() {
		if qaLocked {
			ws.qaMu.Unlock()
		}
	}()
	if len(p.QADocument) > 0 && string(p.QADocument) != "null" {
		if err := decodeWorkspace(p.QADocument, &qaDoc); err != nil {
			return nil, err
		}
		if qaDoc == nil || !swarm.ValidText(qaDoc.Path, 4096) || len(qaDoc.Title) > 500 {
			return nil, errors.New("invalid QA document")
		}
		path := qaDoc.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err := confine("Write", path)
		if err != nil {
			return nil, err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		qaDoc.Path = filepath.Join(parent, filepath.Base(path))
		if !strings.EqualFold(filepath.Ext(qaDoc.Path), ".md") {
			return nil, errors.New("QA document must be a .md file")
		}
	}
	keys := make([]string, 0, len(p.Members))
	for key, m := range p.Members {
		if !swarm.ValidProfileName(key) || slices.Contains([]string{"conversation", "plan", "qa"}, key) || m == nil {
			return nil, errors.New("invalid member key; use member_control to end members")
		}
		if !swarm.ValidText(m.Name, 120) || !swarm.ValidText(m.Instructions, 30000) || !slices.Contains([]string{"resident", "ephemeral"}, m.Lifetime) || m.Lifetime == "ephemeral" && !swarm.ValidText(m.Task, 32768) || len(m.Task) > 32768 {
			return nil, errors.New("invalid member definition")
		}
		if m.Package != "" && !swarm.ValidProfileName(m.Package) || !slices.Contains([]string{"", "architect", "implementer", "reviewer"}, m.Role) {
			return nil, errors.New("invalid member package/role")
		}
		if m.Cwd == "" {
			m.Cwd = h.cwd
		}
		cwd, err := confine("Read", m.Cwd)
		if err != nil {
			return nil, err
		}
		m.Cwd = cwd
		info, err := os.Stat(cwd)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("member cwd must be a directory")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, profile := range p.Profiles {
		if profile != nil {
			if err := knownProvider(profile.Provider); err != nil {
				return nil, err
			}
		}
	}
	// Never hold the projection lock while path approval can wait on a human.
	// Read/claim under the same lock as export to avoid importing a stale write.
	if qaDoc != nil {
		ws.qaMu.Lock()
		qaLocked = true
		var err error
		qaRestore, err = readQAArchive(qaDoc.Path)
		if err != nil {
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
			if _, err := s.Setup(h.sessionID, h.agent, h.cwd, p.Workspace.Name, root, nil); err != nil {
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
			if len(p.QADocument) > 0 {
				if qaDoc != nil {
					for _, other := range allWorkspaces {
						if other.ID != w.ID && other.State != "ended" && other.QADocument != nil && (other.QADocument.Path == qaDoc.Path || qaRestore != nil && qaRestore.DocumentID != "" && qaOwner(&other) == qaRestore.DocumentID) {
							return errors.New("QA document is used by another workspace")
						}
					}
				}
				if qaDoc != nil && (w.QADocument == nil || w.QADocument.Path != qaDoc.Path) {
					source := qaRestore
					// The store may contain newer history than a failed final export.
					for _, other := range allWorkspaces {
						if other.ID != w.ID && other.QADocument != nil && other.QADocument.Path == qaDoc.Path && other.State == "ended" {
							restored := archiveQA(&other)
							source = &restored
							break
						}
					}
					if source != nil && source.DocumentID != qaOwner(w) {
						if err := restoreQA(w, source); err != nil {
							return err
						}
					}
					if qaRestore != nil {
						w.QAOriginalHash = qaRestore.OriginalHash
					}
				}
				w.QADocument = qaDoc
			}
			if len(p.Document) > 0 {
				w.Document = doc
			}
			if w.QADocument != nil && w.Document != nil && w.QADocument.Path == w.Document.Path {
				return errors.New("QA output must differ from the plan document")
			}
			if p.Plan != nil {
				ids := make([]string, 0, len(p.Plan.Items))
				for id := range p.Plan.Items {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					patch := p.Plan.Items[id]
					idx := slices.IndexFunc(w.Items, func(it swarm.Item) bool { return it.ID == id })
					if string(patch) == "null" {
						if idx < 0 {
							return fmt.Errorf("unknown plan item %s", id)
						}
						w.Items = append(w.Items[:idx], w.Items[idx+1:]...)
						continue
					}
					var fields struct {
						Text  *string `json:"text"`
						Emoji *string `json:"emoji"`
						State *string `json:"state"`
					}
					if err := decodeWorkspace(patch, &fields); err != nil {
						return err
					}
					if idx < 0 {
						w.Items = append(w.Items, swarm.Item{ID: id, State: "pending"})
						idx = len(w.Items) - 1
					}
					it := &w.Items[idx]
					if fields.Text != nil {
						it.Text = *fields.Text
					}
					if fields.Emoji != nil {
						it.Emoji = *fields.Emoji
					}
					if fields.State != nil {
						it.State = *fields.State
					}
					it.Revision = w.PlanRevision + 1
				}
				if p.Plan.Order != nil {
					if len(p.Plan.Order) != len(w.Items) {
						return errors.New("plan order must include each ID exactly once")
					}
					ordered := []swarm.Item{}
					seen := map[string]bool{}
					for _, id := range p.Plan.Order {
						idx := slices.IndexFunc(w.Items, func(it swarm.Item) bool { return it.ID == id })
						if idx < 0 || seen[id] {
							return errors.New("invalid plan order")
						}
						seen[id] = true
						ordered = append(ordered, w.Items[idx])
					}
					w.Items = ordered
				}
				if err := swarm.ValidateItems(w.Items); err != nil {
					return err
				}
				w.PlanRevision++
			}
			for _, key := range keys {
				spec := p.Members[key]
				if prior := swarm.GetMember(w, key); prior != nil {
					if prior.State == "ended" {
						return fmt.Errorf("member %s is ended; use a new key for replacement", key)
					}
					if prior.Name != spec.Name || spec.Profile != "" && prior.Profile != spec.Profile || prior.Cwd != spec.Cwd || prior.Instructions != spec.Instructions || prior.Lifetime != spec.Lifetime || prior.CanSpawn != spec.CanSpawn || prior.Package != spec.Package || prior.Role != spec.Role || prior.InitialTask != spec.Task {
						return fmt.Errorf("member %s already exists with different settings; end and replace explicitly", key)
					}
					// Profile edits affect future launches; explicit launch overrides must still match.
					old := prior.LaunchSettings
					if old == nil || spec.Capability != "" && old.Capability != spec.Capability || spec.Approval != "" && old.Approval != spec.Approval || spec.Provider != "" && old.Provider != spec.Provider || spec.Model != "" && old.Model != spec.Model || spec.Thinking != "" && old.Thinking != spec.Thinking {
						return errors.New("existing member launch settings differ")
					}
					for id, val := range spec.Configs {
						if old.Configs[id] != val {
							return errors.New("existing member adapter settings differ")
						}
					}
					continue
				}
				live := 0
				for _, m := range w.Members {
					if m.State != "ended" {
						live++
					}
					// Unique within a package, not the workspace: with titled
					// packages a member's name is its role, so every package
					// has an "Implementer". Lookup is by ID or key, never name.
					if m.Name == spec.Name && m.Package == spec.Package && m.State != "ended" {
						return errors.New("member name already exists in this package")
					}
				}
				if live >= w.MaxMembers {
					return errors.New("workspace member limit reached")
				}
				profile, settings, err := swarm.ResolveProfile(w, spec.Profile, swarm.AgentProfile{Capability: spec.Capability, Approval: spec.Approval, Provider: spec.Provider, Model: spec.Model, Thinking: spec.Thinking, Configs: spec.Configs}, h.agent)
				if err != nil {
					return err
				}
				// Children stay within the launcher's authority: only a session
				// that is itself auto-approved can launch one that is, so no agent
				// grants a teammate what the human has not granted it.
				if settings.Approval == "auto" && !callerAuto {
					return errors.New(`approval "auto" requires the configuring session to be auto-approved itself`)
				}
				if settings.Capability == "reviewer" && spec.CanSpawn {
					return errors.New("reviewer capability cannot spawn agents")
				}
				if err := knownProvider(settings.Provider); err != nil {
					return err
				}
				w.Members = append(w.Members, swarm.Member{ID: swarm.ID(), Key: key, Name: spec.Name, Profile: profile, Provider: settings.Provider, LaunchSettings: &settings, Cwd: spec.Cwd, Instructions: spec.Instructions, InitialTask: spec.Task, Lifetime: spec.Lifetime, State: "pending", Creator: creator.ID, CanSpawn: spec.CanSpawn, Package: spec.Package, Role: spec.Role})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := s.ClaimQADocument(h.sessionID); err != nil {
			return nil, err
		}
		w := s.View(h.sessionID)
		members := map[string]string{}
		for _, key := range keys {
			members[key] = swarm.GetMember(w, key).ID
		}
		return map[string]any{"workspace_id": w.ID, "revision": w.Revision, "members": members, "preview": p.Preview, "configuration": map[string]any{"name": w.Name, "project_root": w.Root, "profiles": w.Profiles, "packages": w.Packages, "default_profile": w.DefaultProfile, "items": w.Items, "document": w.Document, "qa_document": w.QADocument, "max_active": w.MaxActive, "max_members": w.MaxMembers}}, nil
	})
	if qaLocked {
		ws.qaMu.Unlock()
		qaLocked = false
	}
	if err != nil || p.Preview {
		return result, err
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
	outcomes := map[string]any{}
	for _, key := range keys {
		w := ws.store.View(h.sessionID)
		if w == nil || w.ID != receipt.WorkspaceID {
			outcomes[key] = map[string]string{"state": "ended"}
			continue
		}
		m := swarm.GetMember(w, receipt.Members[key])
		if m == nil {
			continue
		}
		if m.State == "pending" {
			_, startErr := ws.spawn(ctx, h, m.ID)
			if startErr != nil {
				state := "ended"
				if current := swarm.GetMember(ws.store.View(h.sessionID), m.ID); current != nil {
					state = current.State
				}
				outcomes[key] = map[string]string{"state": state, "error": startErr.Error()}
				continue
			}
		}
		current := ws.store.View(h.sessionID)
		if current != nil {
			outcomes[key] = swarm.GetMember(current, key)
		}
	}
	return map[string]any{"receipt": result, "launches": outcomes}, nil
}
func knownProvider(provider string) error {
	for _, a := range adapters {
		if a.ID == provider {
			return nil
		}
	}
	return fmt.Errorf("unknown provider %q", provider)
}
