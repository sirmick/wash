package agentproto

// Catalog and launch-preference messages: what the Agents window's Catalog
// tab and the launcher's Permissions row write to agents.json through agentd
// (catalogs.go). The result comes back on the next roster push
// (State.Catalogs, State.Launch); a save also answers the asker so it can
// show an error.

// AgentSetCatalog stores one catalog under agents.json `catalogs`, whole,
// as the Catalog tab holds it. For a built-in catalog this is its override;
// for any other id it is the user's own.
type AgentSetCatalog struct {
	ID      string      `json:"id"`
	Catalog CatalogSpec `json:"catalog"`
}

// CatalogSpec is a catalog as written: a name and either an adapter (an
// auto catalog, listing what that adapter offers) or three slots.
type CatalogSpec struct {
	Name       string `json:"name"`
	Adapter    string `json:"adapter,omitempty"`
	Connection string `json:"connection,omitempty"`
	// Slots is keyed by slot name (frontier, coding, small); all three are
	// required for a curated catalog, and none is given for an auto one.
	Slots map[string]SlotSpec `json:"slots,omitempty"`
}

// SlotSpec is one slot as written: a model on an adapter. Effort and
// Connection may be empty (the adapter's default effort; the adapter
// direct).
type SlotSpec struct {
	Adapter    string `json:"adapter"`
	Connection string `json:"connection,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

// AgentDeleteCatalog removes a catalog from agents.json: a built-in goes
// back to what wash ships, the user's own is gone.
type AgentDeleteCatalog struct {
	ID string `json:"id"`
}

// CatalogSaved answers AgentSetCatalog and AgentDeleteCatalog.
type CatalogSaved struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

// AgentSetLaunch stores the launcher's remembered permission default
// (State.Launch), whole.
type AgentSetLaunch struct {
	Launch LaunchPrefs `json:"launch"`
}

func init() {
	register(Spec{Kind: "agent_set_catalog", Dir: Request, Payload: AgentSetCatalog{}, From: "a manager (manager_subscribe)", Reply: "catalog_saved",
		Doc: "Store a catalog, whole, in agents.json."})
	register(Spec{Kind: "agent_delete_catalog", Dir: Request, Payload: AgentDeleteCatalog{}, From: "a manager (manager_subscribe)", Reply: "catalog_saved",
		Doc: "Remove a catalog from agents.json: a built-in reverts, the user's own is deleted."})
	register(Spec{Kind: "agent_set_launch", Dir: Request, Payload: AgentSetLaunch{}, From: "a manager (manager_subscribe)",
		Doc: "Store the launcher's remembered permission default."})

	register(Spec{Kind: "catalog_saved", Dir: Push, Payload: CatalogSaved{}, From: "the asker",
		Doc: "The outcome of storing or removing a catalog."})
}
