package agentd

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// workspacePatch sends only changed frame fields and keyed plan items. The
// sequence guard lets a remounted view request a fresh snapshot instead of
// applying a delta against stale state.
func workspacePatch(old, next []byte, base, sequence int64) map[string]any {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(old, &a) != nil || json.Unmarshal(next, &b) != nil {
		return nil
	}
	var aw, bw map[string]json.RawMessage
	if json.Unmarshal(a["workspace"], &aw) != nil || json.Unmarshal(b["workspace"], &bw) != nil || aw == nil || bw == nil || !bytes.Equal(aw["id"], bw["id"]) {
		return nil
	}
	frame := map[string]any{}
	workspace := map[string]any{}
	for key, v := range b {
		if key != "workspace" && key != "kind" && key != "key" && !bytes.Equal(a[key], v) {
			frame[key] = v
		}
	}
	for key := range a {
		if _, ok := b[key]; !ok && key != "workspace" {
			frame[key] = nil
		}
	}
	for key, v := range bw {
		if key != "items" && !bytes.Equal(aw[key], v) {
			workspace[key] = v
		}
	}
	for key := range aw {
		if _, ok := bw[key]; !ok {
			workspace[key] = nil
		}
	}
	patch := map[string]any{"kind": "workspace_patch", "key": b["key"], "base": base, "sequence": sequence, "frame": frame, "workspace": workspace}
	if !bytes.Equal(aw["items"], bw["items"]) {
		var before, after []map[string]any
		_ = json.Unmarshal(aw["items"], &before)
		_ = json.Unmarshal(bw["items"], &after)
		prior := map[string]map[string]any{}
		oldOrder := []string{}
		order := []string{}
		upsert := []any{}
		remove := []string{}
		for _, item := range before {
			id, _ := item["id"].(string)
			prior[id] = item
			oldOrder = append(oldOrder, id)
		}
		for _, item := range after {
			id, _ := item["id"].(string)
			order = append(order, id)
			if !reflect.DeepEqual(prior[id], item) {
				upsert = append(upsert, item)
			}
			delete(prior, id)
		}
		for id := range prior {
			remove = append(remove, id)
		}
		items := map[string]any{"upsert": upsert, "remove": remove}
		if !reflect.DeepEqual(oldOrder, order) {
			items["order"] = order
		}
		patch["items"] = items
	}
	return patch
}
