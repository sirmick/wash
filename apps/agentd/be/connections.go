package agentd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"sort"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
)

// Connections: named ways to reach an adapter (agentpolicy.Connection). The
// built-in ones are data in catalogs.json, beside the catalogs that use them;
// agents.json's `connections` replaces one by name or adds more. An
// adapter's own id is its direct connection and is never an entry.

//go:embed catalogs.json
var launchDataJSON []byte

// launchData is catalogs.json.
type launchData struct {
	Keys        map[string]keySpec                `json:"keys"`
	Connections map[string]agentpolicy.Connection `json:"connections"`
	Catalogs    map[string]Catalog                `json:"catalogs"`
}

// keySpec describes a key the launcher offers to store: what to call it, and
// the URL that checks one (a GET with it as the bearer token).
type keySpec struct {
	Name    string `json:"name"`
	TestURL string `json:"test_url,omitempty"`
}

// builtinLaunch is catalogs.json, decoded once. The file is in the binary,
// so a malformed one is a build defect, not a runtime condition.
var builtinLaunch = func() launchData {
	var d launchData
	if err := json.Unmarshal(launchDataJSON, &d); err != nil {
		panic("agentd: catalogs.json: " + err.Error())
	}
	return d
}()

// keyStore reads the key store fresh, like hostedPolicy reads agents.json:
// a key set a moment ago applies to the next launch. Indirected for tests.
var keyStore = func() map[string]string {
	return agentpolicy.LoadKeys(agentpolicy.KeysPath())
}

// connections is the built-in set with the user's merged over it by name.
func connections(pol agentpolicy.Policy) map[string]agentpolicy.Connection {
	out := maps.Clone(builtinLaunch.Connections)
	maps.Copy(out, pol.Connections)
	return out
}

// publishConnections is every connection, sorted by id, for the Setup tab
// to offer a slot.
func publishConnections(pol agentpolicy.Policy) []agentproto.ConnectionView {
	all := connections(pol)
	out := make([]agentproto.ConnectionView, 0, len(all))
	for id, c := range all {
		out = append(out, agentproto.ConnectionView{ID: id, Adapter: c.Adapter, Key: c.Key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// knownConnection checks that connection name exists and is for provider.
// "" is the provider direct and always fine.
func knownConnection(pol agentpolicy.Policy, provider, name string) error {
	if name == "" {
		return nil
	}
	c, ok := connections(pol)[name]
	if !ok {
		return fmt.Errorf("unknown connection %q", name)
	}
	if c.Adapter != provider {
		return fmt.Errorf("connection %q is for %s, not %s", name, c.Adapter, provider)
	}
	return nil
}

// connectionEnv is what connection `name` adds to adapter agentID's
// environment: its own env, then its key under each of its key_env names.
// "" is the adapter direct and adds nothing. An unknown connection, one for
// another adapter, or one whose key is not set is an error rather than a
// launch that would fail later with the provider's less helpful words.
func connectionEnv(pol agentpolicy.Policy, keys map[string]string, agentID, name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	if err := knownConnection(pol, agentID, name); err != nil {
		return nil, err
	}
	c := connections(pol)[name]
	env := make([]string, 0, len(c.Env)+len(c.KeyEnv))
	names := make([]string, 0, len(c.Env))
	for k := range c.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		env = append(env, k+"="+c.Env[k])
	}
	if c.Key != "" {
		key := keys[c.Key]
		if key == "" {
			return nil, fmt.Errorf("connection %q needs the %s key, which is not set", name, c.Key)
		}
		for _, k := range c.KeyEnv {
			env = append(env, k+"="+key)
		}
	}
	return env, nil
}

// connectionStatus says whether connection name can start adapter agentID
// here, and if not, why, in words for a greyed launcher row.
func connectionStatus(pol agentpolicy.Policy, keys map[string]string, agentID, name string) (bool, string) {
	a, ok := adapterByID(agentID)
	if !ok {
		return false, "unknown adapter " + agentID
	}
	if _, _, note, ok := a.launchWith(pol.AgentFor(agentID)); !ok {
		return false, a.Name + ": " + note
	}
	if _, err := connectionEnv(pol, keys, agentID, name); err != nil {
		if c, known := connections(pol)[name]; known && c.Key != "" && keys[c.Key] == "" {
			return false, "no " + c.Key + " key set"
		}
		return false, err.Error()
	}
	return true, ""
}
