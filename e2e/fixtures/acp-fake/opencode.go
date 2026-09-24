package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Run under the name `opencode`, the fake answers the way OpenCode 1.18.32
// did when checked against a real install (docs/AGENT_APP.md §6): its model
// option lists openrouter/* models only when OPENROUTER_API_KEY is in its
// environment, and choosing one adds an effort option that starts at "low".
// That is what lets the e2e suite start the shipped OpenRouter stack without
// a network or a key worth anything.

func isOpencode() bool { return filepath.Base(os.Args[0]) == "opencode" }

// openrouterModels are the shipped OpenRouter stack's four models.
var openrouterModels = []string{
	"openrouter/~anthropic/claude-opus-latest",
	"openrouter/deepseek/deepseek-v4-pro-0813",
	"openrouter/z-ai/glm-5.3",
	"openrouter/~deepseek/deepseek-v4-flash-latest",
}

var opencodeModel = "opencode/big-pickle"
var opencodeEffort = ""

func opencodeConfigOptions() []any {
	models := []any{map[string]any{"value": "opencode/big-pickle", "name": "OpenCode Zen/Big Pickle"}}
	if os.Getenv("OPENROUTER_API_KEY") != "" {
		for _, m := range openrouterModels {
			models = append(models, map[string]any{"value": m, "name": m})
		}
	}
	opts := []any{map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": opencodeModel, "options": models}}
	if opencodeEffort != "" {
		levels := []any{}
		for _, l := range []string{"low", "high", "max", "default"} {
			levels = append(levels, map[string]any{"value": l, "name": l})
		}
		opts = append(opts, map[string]any{"id": "effort", "name": "Effort", "category": "thought_level", "type": "select", "currentValue": opencodeEffort, "options": levels})
	}
	return append(opts, map[string]any{"id": "mode", "name": "Session Mode", "category": "mode", "type": "select", "currentValue": "build",
		"options": []any{map[string]any{"value": "build", "name": "build"}, map[string]any{"value": "plan", "name": "plan"}}})
}

func opencodeSetConfig(id, value string) (any, error) {
	switch id {
	case "model":
		offered := value == "opencode/big-pickle"
		for _, m := range openrouterModels {
			offered = offered || os.Getenv("OPENROUTER_API_KEY") != "" && m == value
		}
		if !offered {
			return nil, fmt.Errorf("model not found: %s", value)
		}
		opencodeModel = value
		opencodeEffort = ""
		if strings.HasPrefix(value, "openrouter/") {
			opencodeEffort = "low"
		}
	case "effort":
		if opencodeEffort == "" {
			return nil, fmt.Errorf("no effort levels for this model")
		}
		opencodeEffort = value
	case "mode":
	default:
		return nil, fmt.Errorf("unknown config option: %s", id)
	}
	return map[string]any{"configOptions": opencodeConfigOptions()}, nil
}

// keyReport says which credentials and config reached this process, for a
// spec to check that a connection's environment arrived. Only the last four
// characters of a key, so a real one pasted by mistake is not echoed.
func keyReport() string {
	last4 := func(v string) string {
		if len(v) < 4 {
			return v
		}
		return v[len(v)-4:]
	}
	return fmt.Sprintf("KEYS<<openrouter=%s auth-token=%s opencode-config=%t>>",
		last4(os.Getenv("OPENROUTER_API_KEY")), last4(os.Getenv("ANTHROPIC_AUTH_TOKEN")), os.Getenv("OPENCODE_CONFIG_CONTENT") != "")
}
