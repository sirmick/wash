package agentd

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
)

// What each adapter offers — its approval presets, its models, its effort
// levels — is only ever learned from a live session: ACP has no way to ask
// an adapter without starting one. So agentd remembers what the last
// session of each adapter reported, across restarts, and publishes it
// (State.AdapterOptions). That is what lets the Stacks tab offer a model
// list instead of a text box, the Permissions row name the presets, and a
// stale tier be greyed before anyone starts it. It is a memory, not a
// promise: the next session may offer something else, and the launch path
// still checks every value against what that session actually offers
// (configureWorkspaceSession).
//
// Kept in $XDG_STATE_HOME/wash/agent-adapters.json: state, like the session
// history, not configuration.

var (
	adapterMemMu sync.Mutex
	adapterMem   map[string]agentproto.AdapterOptions
)

func adapterMemoryPath() string {
	dir := filepath.Dir(transcriptDir())
	if dir == "." || dir == "" {
		return ""
	}
	return filepath.Join(dir, "agent-adapters.json")
}

func loadAdapterMemory() map[string]agentproto.AdapterOptions {
	adapterMemMu.Lock()
	defer adapterMemMu.Unlock()
	if adapterMem == nil {
		adapterMem = map[string]agentproto.AdapterOptions{}
		if data, err := os.ReadFile(adapterMemoryPath()); err == nil {
			if err := json.Unmarshal(data, &adapterMem); err != nil {
				log.Printf("agentd: %s: %v", adapterMemoryPath(), err)
				adapterMem = map[string]agentproto.AdapterOptions{}
			}
		}
	}
	return adapterMem
}

// rememberAdapter records what a session of adapter just reported. The
// current values are stripped: they are that session's, and keeping them
// would make every start of the same adapter look like a change.
func rememberAdapter(adapter string, info acp.Implementation, modes []acp.SessionMode, configs []acp.ConfigOption) {
	if adapter == "" {
		return
	}
	next := agentproto.AdapterOptions{Adapter: adapter, Version: info.Version, Modes: publicModes(modes), Configs: publicConfigs(configs)}
	for i := range next.Configs {
		next.Configs[i].Current = ""
	}
	mem := loadAdapterMemory()
	adapterMemMu.Lock()
	prev, had := mem[adapter]
	changed := !had || !sameAdapterOptions(prev, next)
	if changed {
		mem[adapter] = next
	}
	adapterMemMu.Unlock()
	if !changed {
		return
	}
	saveAdapterMemory()
	mutateState(func(s *agentproto.State) { s.AdapterOptions = publishAdapterOptions() })
}

func sameAdapterOptions(a, b agentproto.AdapterOptions) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// publishAdapterOptions is the memory, sorted by adapter for a stable push.
func publishAdapterOptions() []agentproto.AdapterOptions {
	mem := loadAdapterMemory()
	adapterMemMu.Lock()
	out := make([]agentproto.AdapterOptions, 0, len(mem))
	for _, o := range mem {
		out = append(out, o)
	}
	adapterMemMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Adapter < out[j].Adapter })
	return out
}

func saveAdapterMemory() {
	path := adapterMemoryPath()
	if path == "" {
		return
	}
	adapterMemMu.Lock()
	data, err := json.MarshalIndent(adapterMem, "", "  ")
	adapterMemMu.Unlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("agentd: adapter memory dir: %v", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-adapters-*.json")
	if err != nil {
		log.Printf("agentd: adapter memory temp: %v", err)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(name, path); err != nil {
		log.Printf("agentd: adapter memory save: %v", err)
	}
}

// resetAdapterMemoryForTest drops the memory so a test with its own
// XDG_STATE_HOME starts empty.
func resetAdapterMemoryForTest() {
	adapterMemMu.Lock()
	adapterMem = nil
	adapterMemMu.Unlock()
}
