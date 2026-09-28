package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Catalogs: where a model comes from. The launcher picks a catalog and a
// model instead of an adapter and a model id; a workspace has one catalog,
// and its members say "model":"coding" instead of carrying model strings.
//
// A catalog is one of two things:
//
//   - an adapter's own list ("Anthropic": Claude Code direct, "OpenRouter":
//     OpenCode through the OpenRouter connection). Its models are whatever
//     that adapter reported the last time it ran here (adapter memory), so
//     nothing is pinned and nothing goes stale;
//   - a curated set of three slots, frontier, coding and small ("Anthropic
//     pro", "OpenRouter budget"), each a model on an adapter with an effort.
//     A slot is a model and nothing else: what a session may DO (read-only,
//     no subagents, auto-approval) is a property of one launch, never of the
//     catalog.
//
// A slot is a swarm.AgentProfile, the type a member's launch settings use.
// The defaults are data (catalogs.json), because model names churn faster
// than code should; agents.json's `catalogs` replaces a catalog by id, whole,
// or adds one, which is what the Agents window's Catalog tab writes.

// Slots, in launcher order. A curated catalog starts on frontier by default.
var slotNames = []string{"frontier", "coding", "small"}

const defaultSlot = "frontier"

// Catalog is one catalog as catalogs.json and agents.json write it.
type Catalog struct {
	Name string `json:"name"`
	// Adapter and Connection make an auto catalog; Slots a curated one.
	Adapter    string                        `json:"adapter,omitempty"`
	Connection string                        `json:"connection,omitempty"`
	Slots      map[string]swarm.AgentProfile `json:"slots,omitempty"`
}

func (c Catalog) auto() bool { return len(c.Slots) == 0 }

// loadCatalogs is the built-in catalogs with agents.json's over them, by
// id: an entry there replaces the whole catalog, so the file says exactly
// what the Catalog tab showed when it was saved. A catalog that fails
// validation is returned with its error rather than dropped, so the launcher
// can grey it with the reason instead of it silently vanishing.
func loadCatalogs(pol agentpolicy.Policy) (map[string]Catalog, map[string]error) {
	catalogs := map[string]Catalog{}
	for id, c := range builtinLaunch.Catalogs {
		catalogs[id] = Catalog{Name: c.Name, Adapter: c.Adapter, Connection: c.Connection, Slots: maps.Clone(c.Slots)}
	}
	bad := map[string]error{}
	for id, raw := range pol.Catalogs {
		var c Catalog
		if err := decodeWorkspace(raw, &c); err != nil {
			bad[id] = fmt.Errorf("agents.json catalog %q: %w", id, err)
			continue
		}
		catalogs[id] = c
	}
	for id, c := range catalogs {
		if _, done := bad[id]; done {
			continue
		}
		if err := validateCatalog(pol, c); err != nil {
			bad[id] = fmt.Errorf("catalog %q: %w", id, err)
		}
	}
	return catalogs, bad
}

// validateCatalog: a name, and either an adapter (with a connection that
// exists for it) or all three slots, each passing the member profile rules
// with adapters and connections that exist and no permission settings.
// Auto-approval is granted per launch by a session that has it; a global
// catalog handing it to every launch would be a standing yes nobody gave.
func validateCatalog(pol agentpolicy.Policy, c Catalog) error {
	if !swarm.ValidText(c.Name, 80) {
		return errors.New("needs a name")
	}
	if c.auto() {
		if err := knownProvider(c.Adapter); err != nil {
			return err
		}
		return knownConnection(pol, c.Adapter, c.Connection)
	}
	if c.Adapter != "" || c.Connection != "" {
		return errors.New("a catalog is an adapter or three slots, not both")
	}
	for id := range c.Slots {
		if !slices.Contains(slotNames, id) {
			return fmt.Errorf("unknown slot %q; slots are %v", id, slotNames)
		}
	}
	for _, id := range slotNames {
		s, ok := c.Slots[id]
		if !ok {
			return fmt.Errorf("has no %s slot", id)
		}
		if err := swarm.ValidateProfile(s); err != nil {
			return fmt.Errorf("slot %s: %w", id, err)
		}
		if err := knownProvider(s.Provider); err != nil {
			return fmt.Errorf("slot %s: %w", id, err)
		}
		if s.Approval != "" || s.Capability != "" || s.Subagents != "" || len(s.Configs) > 0 {
			return fmt.Errorf("slot %s: a slot is a model only; approval, capability, subagents and configs are a member's", id)
		}
		if err := knownConnection(pol, s.Provider, s.Connection); err != nil {
			return fmt.Errorf("slot %s: %w", id, err)
		}
	}
	return nil
}

// resolveCatalog is the launch settings for a model of one catalog: a slot
// of a curated catalog by name (frontier when empty), else a model id on
// the catalog's adapter — an auto catalog's, or a curated catalog's
// frontier slot's adapter and connection. A copy the caller may change.
func resolveCatalog(catalogs map[string]Catalog, bad map[string]error, id, model string) (swarm.AgentProfile, error) {
	if err := bad[id]; err != nil {
		return swarm.AgentProfile{}, err
	}
	c, ok := catalogs[id]
	if !ok {
		return swarm.AgentProfile{}, fmt.Errorf("unknown catalog %q", id)
	}
	if c.auto() {
		// An adapter's own list has no slots. Passed through, "coding"
		// reached the adapter as a model id: preview passed and the launch
		// failed. Refused here, preview says it, naming the catalogs that
		// do have the slot on this adapter.
		if slices.Contains(slotNames, model) {
			return swarm.AgentProfile{}, fmt.Errorf("catalog %q is %s's own model list and has no slots; name a model id it offers (view=state config_options), or a catalog with slots%s", id, c.Adapter, curatedFor(catalogs, bad, c.Adapter))
		}
		return swarm.AgentProfile{Provider: c.Adapter, Connection: c.Connection, Model: model}, nil
	}
	if model == "" {
		model = defaultSlot
	}
	if s, ok := c.Slots[model]; ok {
		s.Configs = maps.Clone(s.Configs)
		return s, nil
	}
	base := c.Slots[defaultSlot]
	return swarm.AgentProfile{Provider: base.Provider, Connection: base.Connection, Model: model}, nil
}

// curatedFor lists the valid catalogs with slots whose frontier slot runs
// on adapter, as ": a, b" for an error message, or "" when there are none.
func curatedFor(catalogs map[string]Catalog, bad map[string]error, adapter string) string {
	var ids []string
	for id, c := range catalogs {
		if !c.auto() && bad[id] == nil && c.Slots[defaultSlot].Provider == adapter {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return ": " + strings.Join(ids, ", ")
}

// autoCatalogFor is the catalog a session started with none is on: its
// adapter's own list, through the same connection. A session `wash ai
// --agent` started leads a workspace like any other, and its members need
// a catalog to name a model against.
func autoCatalogFor(catalogs map[string]Catalog, adapter, connection string) string {
	ids := make([]string, 0, len(catalogs))
	for id := range catalogs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if c := catalogs[id]; c.auto() && c.Adapter == adapter && c.Connection == connection {
			return id
		}
	}
	return ""
}

// publishCatalogs is every catalog with what this box can start, sorted by
// id. An invalid catalog still lists what it has, so the Catalog tab can
// show what is wrong and let it be fixed rather than only saying that it
// is.
func publishCatalogs(pol agentpolicy.Policy, keys map[string]string) []agentproto.CatalogView {
	catalogs, bad := loadCatalogs(pol)
	ids := make([]string, 0, len(catalogs))
	for id := range catalogs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]agentproto.CatalogView, 0, len(ids))
	for _, id := range ids {
		c := catalogs[id]
		_, builtin := builtinLaunch.Catalogs[id]
		_, inPolicy := pol.Catalogs[id]
		v := agentproto.CatalogView{ID: id, Name: c.Name, Adapter: c.Adapter, Connection: c.Connection, Available: true, Builtin: builtin, Overridden: builtin && inPolicy}
		if v.Name == "" {
			v.Name = id
		}
		invalid := bad[id] != nil
		if invalid {
			v.Available, v.Note = false, bad[id].Error()
		} else if c.auto() {
			v.Available, v.Note = connectionStatus(pol, keys, c.Adapter, c.Connection)
		}
		for _, name := range slotNames {
			s, ok := c.Slots[name]
			if !ok {
				continue
			}
			sv := agentproto.SlotView{Slot: name, Adapter: s.Provider, Connection: s.Connection, Model: s.Model, Effort: s.Effort}
			if invalid {
				sv.Note = v.Note
				v.Slots = append(v.Slots, sv)
				continue
			}
			sv.Available, sv.Note = connectionStatus(pol, keys, s.Provider, s.Connection)
			if !sv.Available && v.Available {
				v.Available, v.Note = false, sv.Note
			}
			v.Slots = append(v.Slots, sv)
		}
		out = append(out, v)
	}
	return out
}

// startProfile is what a start request launches: the catalog's model with
// Advanced's settings on top, or, with no catalog, an adapter on its
// defaults (wash ai --agent).
func startProfile(pol agentpolicy.Policy, req agentproto.AgentStart) (swarm.AgentProfile, sessionLaunch, error) {
	var p swarm.AgentProfile
	switch {
	case req.Catalog != "":
		catalogs, bad := loadCatalogs(pol)
		var err error
		if p, err = resolveCatalog(catalogs, bad, req.Catalog, req.Model); err != nil {
			return p, sessionLaunch{}, err
		}
	case req.Agent != "":
		p = swarm.AgentProfile{Provider: req.Agent, Model: req.Model}
	default:
		return p, sessionLaunch{}, errors.New("choose a catalog, or an agent")
	}
	if len(req.Configs) > 0 {
		if p.Configs == nil {
			p.Configs = map[string]string{}
		}
		maps.Copy(p.Configs, req.Configs)
	}
	// A catalog carries no permissions (validateCatalog), so nothing here
	// can restrict or auto-approve; the launch's own Mode and Yolo are
	// applied once the session exists (startSession).
	return p, sessionLaunch{connection: p.Connection, catalog: req.Catalog, model: req.Model}, nil
}

// catalogFromSpec is a catalog as the Catalog tab writes it, in the shape
// catalogs.json and agents.json hold.
func catalogFromSpec(spec agentproto.CatalogSpec) Catalog {
	c := Catalog{Name: spec.Name, Adapter: spec.Adapter, Connection: spec.Connection}
	if len(spec.Slots) > 0 {
		c.Slots = map[string]swarm.AgentProfile{}
		for id, s := range spec.Slots {
			c.Slots[id] = swarm.AgentProfile{Provider: s.Adapter, Connection: s.Connection, Model: s.Model, Effort: s.Effort}
		}
	}
	return c
}

// setCatalog stores one catalog, whole, under agents.json `catalogs`, after
// the same check the launcher applies: what is saved is what can be offered.
func setCatalog(pol agentpolicy.Policy, id string, spec agentproto.CatalogSpec) error {
	if !swarm.ValidProfileName(id) {
		return errors.New("a catalog id is letters, digits, - and _")
	}
	c := catalogFromSpec(spec)
	if err := validateCatalog(pol, c); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return agentpolicy.Update(agentpolicy.Path(), func(p *agentpolicy.Policy) error {
		if p.Catalogs == nil {
			p.Catalogs = map[string]json.RawMessage{}
		}
		p.Catalogs[id] = raw
		return nil
	})
}

// deleteCatalog removes a catalog from agents.json: a built-in is back to
// what wash ships, the user's own is gone.
func deleteCatalog(id string) error {
	return agentpolicy.Update(agentpolicy.Path(), func(p *agentpolicy.Policy) error {
		if _, ok := p.Catalogs[id]; !ok {
			return fmt.Errorf("agents.json has no catalog %q", id)
		}
		delete(p.Catalogs, id)
		return nil
	})
}

// setLaunch stores the launcher's remembered permission default.
func setLaunch(prefs agentproto.LaunchPrefs) error {
	for adapter, mode := range prefs.Mode {
		if _, ok := adapterByID(adapter); !ok {
			return fmt.Errorf("unknown adapter %q", adapter)
		}
		if !swarm.ValidText(mode, 80) {
			return fmt.Errorf("invalid mode for %s", adapter)
		}
	}
	return agentpolicy.Update(agentpolicy.Path(), func(p *agentpolicy.Policy) error {
		p.Launch = nil
		if len(prefs.Mode) > 0 || prefs.Yolo {
			p.Launch = &agentpolicy.LaunchPrefs{Mode: maps.Clone(prefs.Mode), Yolo: prefs.Yolo}
		}
		return nil
	})
}

// publishLaunch is agents.json's launch default as the roster carries it.
func publishLaunch(pol agentpolicy.Policy) agentproto.LaunchPrefs {
	if pol.Launch == nil {
		return agentproto.LaunchPrefs{}
	}
	return agentproto.LaunchPrefs{Mode: maps.Clone(pol.Launch.Mode), Yolo: pol.Launch.Yolo}
}

// The Catalog tab's and the Permissions row's writes. Only a manager may
// change machine configuration, as with keys; the roster push that
// follows is how every window, including the writer, sees the result.
func registerCatalogHandlers(bus *sdk.Bus) {
	sdk.HandleFromVoid(bus, "agent_set_catalog", func(conn *sdk.Conn, _ string, req agentproto.AgentSetCatalog, from wire.Sender) error {
		if !isManager(from.InstanceID) {
			return nil
		}
		reply := agentproto.CatalogSaved{ID: req.ID}
		if err := setCatalog(hostedPolicy(), req.ID, req.Catalog); err != nil {
			reply.Error = err.Error()
		} else {
			log.Printf("agentd: catalog %s saved", req.ID)
			mutateState(func(s *agentproto.State) { refreshLaunchers(s) })
		}
		return agentproto.Send(conn, wire.Recipient{InstanceID: from.InstanceID}, reply)
	})
	sdk.HandleFromVoid(bus, "agent_delete_catalog", func(conn *sdk.Conn, _ string, req agentproto.AgentDeleteCatalog, from wire.Sender) error {
		if !isManager(from.InstanceID) {
			return nil
		}
		reply := agentproto.CatalogSaved{ID: req.ID}
		if err := deleteCatalog(req.ID); err != nil {
			reply.Error = err.Error()
		} else {
			log.Printf("agentd: catalog %s removed from agents.json", req.ID)
			mutateState(func(s *agentproto.State) { refreshLaunchers(s) })
		}
		return agentproto.Send(conn, wire.Recipient{InstanceID: from.InstanceID}, reply)
	})
	sdk.HandleFromVoid(bus, "agent_set_launch", func(_ *sdk.Conn, _ string, req agentproto.AgentSetLaunch, from wire.Sender) error {
		if !isManager(from.InstanceID) {
			return nil
		}
		if err := setLaunch(req.Launch); err != nil {
			log.Printf("agentd: launch default not saved: %v", err)
			return nil
		}
		log.Printf("agentd: launch default mode=%v yolo=%v", req.Launch.Mode, req.Launch.Yolo)
		mutateState(func(s *agentproto.State) { refreshLaunchers(s) })
		return nil
	})
}

// startSession is the one way a new session starts from the launcher: launch
// the adapter through its connection, then apply the settings with the same
// check workspace members get, so a model the adapter does not offer fails
// here, naming the ones it does, rather than running on its default.
func startSession(req agentproto.AgentStart, svcConn *sdk.Conn) (*hosted, error) {
	p, launch, err := startProfile(hostedPolicy(), req)
	if err != nil {
		return nil, err
	}
	h, err := startHostedCapability(p.Provider, req.Cwd, svcConn, launch)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	// The launch's mode BEFORE its model: the adapter's preset by its own
	// name, refused with the names it does offer rather than started on a
	// default nobody chose. Before, because Claude Code re-picks the model
	// when its mode changes (verified live 2026-09-25: haiku, then plan
	// mode, and the adapter reported sonnet a moment after the launch
	// check had passed; plan mode first, then haiku, stayed haiku through
	// a turn). Same order as configureWorkspaceSession gives a member's
	// mode setting.
	if req.Mode != "" {
		if err := h.client.SetMode(ctx, h.sessionID, req.Mode); err != nil {
			h.mu.Lock()
			offered := modeIDs(h.modes)
			h.mu.Unlock()
			h.retire()
			return nil, fmt.Errorf("%s: mode %q: %w; it offers %s", p.Provider, req.Mode, err, offered)
		}
		h.setMode(req.Mode)
	}
	options := h.configsSnapshot()
	effective, err := configureWorkspaceSession(p, options, func(id, value string) ([]acp.ConfigOption, error) {
		res, e := h.client.SetConfigOption(ctx, h.sessionID, id, value)
		if e == nil {
			h.applyConfigs(res.ConfigOptions)
		}
		return res.ConfigOptions, e
	})
	if err != nil {
		h.retire()
		return nil, fmt.Errorf("%s: %w", p.Provider, err)
	}
	// Then wash's auto-approval, which setYolo announces in the transcript
	// like any later switch.
	if req.Yolo {
		h.setYolo(true, "launched with auto-approve on")
	}
	// What the adapter reports now, not what was asked: the two are checked
	// equal above, and this is the line to read when a session "ran on the
	// wrong model".
	h.mu.Lock()
	mode := h.mode
	h.mu.Unlock()
	log.Printf("agentd: session settings key=%s catalog=%s model=%s connection=%s adapter=%s mode=%s yolo=%v effective=%v",
		h.key, launch.catalog, launch.model, launch.connection, p.Provider, mode, req.Yolo, effective)
	return h, nil
}

func modeIDs(modes []acp.SessionMode) string {
	ids := make([]string, 0, len(modes))
	for _, m := range modes {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return "no modes"
	}
	return strings.Join(ids, ", ")
}
