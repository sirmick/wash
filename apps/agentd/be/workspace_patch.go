package agentd

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/internal/swarm"
)

// workspacePatch sends only changed frame fields and the plan's changed nodes. The
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
		if k != "plan" && !bytes.Equal(aw[k], v) {
			patch.Workspace[k] = v
		}
	}
	for k := range aw {
		if _, ok := bw[k]; !ok {
			patch.Workspace[k] = nil
		}
	}
	if !bytes.Equal(aw["plan"], bw["plan"]) {
		var before, after []swarm.Node
		_ = json.Unmarshal(aw["plan"], &before)
		_ = json.Unmarshal(bw["plan"], &after)
		prior := map[string]swarm.Node{}
		oldOrder := []string{}
		order := []string{}
		nodes := &agentproto.WorkspacePlanPatch{Upsert: []swarm.Node{}, Remove: []string{}}
		for _, n := range before {
			prior[n.ID] = n
			oldOrder = append(oldOrder, n.ID)
		}
		for _, n := range after {
			order = append(order, n.ID)
			if old, ok := prior[n.ID]; !ok || !reflect.DeepEqual(old, n) {
				nodes.Upsert = append(nodes.Upsert, n)
			}
			delete(prior, n.ID)
		}
		for id := range prior {
			nodes.Remove = append(nodes.Remove, id)
		}
		if !reflect.DeepEqual(oldOrder, order) {
			nodes.Order = order
		}
		patch.Plan = nodes
	}
	return patch
}
