package agentproto

// Patches: small, latest-wins updates to roster fields that change at
// stream cadence, sent in the Bulk class instead of republishing the whole
// roster. A later full State is authoritative; a patch for a row the
// frontend does not hold is ignored.

// UsagePatch updates rows' context accounting (Row.Used, Row.Size),
// coalesced to at most one send per 500ms.
type UsagePatch struct {
	Rows []UsageRow `json:"rows"`
}

// UsageRow is one row's counters.
type UsageRow struct {
	Key  string `json:"key"`
	Used int64  `json:"used"`
	Size int64  `json:"size"`
}

// PreviewPatch updates rows' transcript previews (Row.Preview) in the
// manager's view.
type PreviewPatch struct {
	Rows []PreviewRow `json:"rows"`
}

// PreviewRow is one row's preview.
type PreviewRow struct {
	Key     string `json:"key"`
	Preview string `json:"preview,omitempty"`
}

func init() {
	register(Spec{Kind: "usage_patch", Dir: Push, Payload: UsagePatch{}, From: "roster subscribers, managers, and each row's controller", Class: Bulk,
		Doc: "New context counters for some rows."})
	register(Spec{Kind: "preview_patch", Dir: Push, Payload: PreviewPatch{}, From: "managers", Class: Bulk,
		Doc: "New transcript previews for some rows."})
}
