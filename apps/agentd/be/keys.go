package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sirmick/wash/internal/agentproto"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Keys the launcher stores for connections (agentpolicy's key store). A key
// crosses the wire once, from the Agents window to agentd when it is saved or
// tested, and never back: the roster carries only whether it is set and its
// last four characters. Nothing here logs a value.

// knownKeys is every key a connection can name: the described ones, and any
// a user's connection names, so a key for a connection of one's own can be
// set from the same screen.
func knownKeys(pol agentpolicy.Policy) map[string]keySpec {
	out := map[string]keySpec{}
	for id, k := range builtinLaunch.Keys {
		out[id] = k
	}
	for _, c := range connections(pol) {
		if _, ok := out[c.Key]; c.Key != "" && !ok {
			out[c.Key] = keySpec{Name: c.Key + " key"}
		}
	}
	return out
}

func publishKeys(pol agentpolicy.Policy, keys map[string]string) []agentproto.KeyView {
	specs := knownKeys(pol)
	out := make([]agentproto.KeyView, 0, len(specs))
	for id, spec := range specs {
		v := agentproto.KeyView{ID: id, Name: spec.Name, Set: keys[id] != "", Testable: spec.TestURL != ""}
		if v.Set {
			v.Hint = agentpolicy.KeyHint(keys[id])
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// keyTestTimeout bounds a key check: the Test button waits on it.
const keyTestTimeout = 10 * time.Second

// testKey checks a key against its test URL. The answer is for a person:
// what the provider said about it, never the key.
func testKey(ctx context.Context, url, key string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "could not reach " + req.URL.Host + ": " + strings.ReplaceAll(err.Error(), key, "…")
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("%s refused this key (%s)", req.URL.Host, res.Status)
	}
	// OpenRouter answers {data:{label, usage, limit, …}}; say what is useful
	// and ignore what is not there.
	var info struct {
		Data struct {
			Label string   `json:"label"`
			Usage *float64 `json:"usage"`
			Limit *float64 `json:"limit"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &info)
	detail := "valid"
	if info.Data.Label != "" {
		detail += ": " + info.Data.Label
	}
	if info.Data.Usage != nil {
		detail += fmt.Sprintf(", $%.2f used", *info.Data.Usage)
		if info.Data.Limit != nil {
			detail += fmt.Sprintf(" of $%.2f", *info.Data.Limit)
		}
	}
	return true, detail
}

func registerKeyHandlers(bus *sdk.Bus) {
	// agent_set_key stores a key, or clears it with an empty value, then
	// republishes: the stacks that need it become available at once.
	sdk.HandleFromVoid(bus, "agent_set_key", func(conn *sdk.Conn, _ string, req agentproto.AgentSetKey, from wire.Sender) error {
		if !isManager(from.InstanceID) {
			return nil
		}
		reply := agentproto.KeySaved{Name: req.Name}
		if _, ok := knownKeys(hostedPolicy())[req.Name]; !ok {
			reply.Error = "unknown key " + req.Name
		} else if err := agentpolicy.SetKey(agentpolicy.KeysPath(), req.Name, strings.TrimSpace(req.Value)); err != nil {
			reply.Error = err.Error()
		} else {
			log.Printf("agentd: key %s set=%v", req.Name, strings.TrimSpace(req.Value) != "")
			mutateState(func(s *agentproto.State) { refreshLaunchers(s) })
		}
		return agentproto.Send(conn, wire.Recipient{InstanceID: from.InstanceID}, reply)
	})

	// agent_test_key checks the typed key, or the stored one when none was
	// typed. Off the bus goroutine: it waits on the network.
	sdk.HandleFromVoid(bus, "agent_test_key", func(conn *sdk.Conn, _ string, req agentproto.AgentTestKey, from wire.Sender) error {
		if !isManager(from.InstanceID) {
			return nil
		}
		spec, ok := knownKeys(hostedPolicy())[req.Name]
		key := strings.TrimSpace(req.Value)
		if key == "" {
			key = keyStore()[req.Name]
		}
		go func() {
			reply := agentproto.KeyTest{Name: req.Name}
			switch {
			case !ok || spec.TestURL == "":
				reply.Detail = "wash has no check for this key"
			case key == "":
				reply.Detail = "no key to test"
			default:
				ctx, cancel := context.WithTimeout(context.Background(), keyTestTimeout)
				defer cancel()
				reply.OK, reply.Detail = testKey(ctx, spec.TestURL, key)
			}
			log.Printf("agentd: key %s test ok=%v", req.Name, reply.OK)
			_ = agentproto.Send(conn, wire.Recipient{InstanceID: from.InstanceID}, reply)
		}()
		return nil
	})
}
