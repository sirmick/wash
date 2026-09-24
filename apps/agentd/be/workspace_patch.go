package agentd

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

// workspacePatch sends only changed frame fields and keyed plan items. The
// sequence guard lets a remounted view request a fresh snapshot instead of
// applying a delta against stale state. old and next are encoded
// agentproto.WorkspaceState frames without a sequence; nil means send the
// whole frame (there is no base, or the workspace itself changed).
func workspacePatch(old, next []byte, base, sequence int64) *agentproto.WorkspacePatch {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(old, &a) != nil || json.Unmarshal(next, &b) != nil {
		return nil
	}
	var aw, bw map[string]json.RawMessage
	if json.Unmarshal(a["workspace"], &aw) != nil || json.Unmarshal(b["workspace"], &bw) != nil || aw == nil || bw == nil || !bytes.Equal(aw["id"], bw["id"]) {
		return nil
	}
	var key string
	_ = json.Unmarshal(b["key"], &key)
	patch := &agentproto.WorkspacePatch{Key: key, Base: base, Sequence: sequence, Frame: map[string]json.RawMessage{}, Workspace: map[string]json.RawMessage{}}
	for k, v := range b {
		if k != "workspace" && k != "key" && k != "sequence" && !bytes.Equal(a[k], v) {
			patch.Frame[k] = v
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok && k != "workspace" {
			patch.Frame[k] = nil
		}
	}
	for k, v := range bw {
		if k != "items" && !bytes.Equal(aw[k], v) {
			patch.Workspace[k] = v
		}
	}
	for k := range aw {
		if _, ok := bw[k]; !ok {
			patch.Workspace[k] = nil
		}
	}
	if !bytes.Equal(aw["items"], bw["items"]) {
		var before, after []swarm.Item
		_ = json.Unmarshal(aw["items"], &before)
		_ = json.Unmarshal(bw["items"], &after)
		prior := map[string]swarm.Item{}
		oldOrder := []string{}
		order := []string{}
		items := &agentproto.WorkspaceItemsPatch{Upsert: []swarm.Item{}, Remove: []string{}}
		for _, item := range before {
			prior[item.ID] = item
			oldOrder = append(oldOrder, item.ID)
		}
		for _, item := range after {
			order = append(order, item.ID)
			if old, ok := prior[item.ID]; !ok || !reflect.DeepEqual(old, item) {
				items.Upsert = append(items.Upsert, item)
			}
			delete(prior, item.ID)
		}
		for id := range prior {
			items.Remove = append(items.Remove, id)
		}
		if !reflect.DeepEqual(oldOrder, order) {
			items.Order = order
		}
		patch.Items = items
	}
	return patch
}
