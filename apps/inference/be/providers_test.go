package inferenceapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shared "github.com/sirmick/wash/pkg/inference"
)

func TestOpenAICompatibleChatCompletions(t *testing.T) {
	var path, auth, content, system string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		// The service's own guard rides as the system message, and the
		// caller's instructions plus the fenced source as the user one.
		for _, m := range body.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				content = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary"}}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`))
	}))
	defer ts.Close()
	r, err := runOpenAI(context.Background(), connection{BaseURL: ts.URL + "/v1/", Model: "m", APIKey: "k"}, shared.Request{Instructions: "Be short", Input: []shared.Part{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" || auth != "Bearer k" {
		t.Fatalf("path=%q auth=%q", path, auth)
	}
	if system != systemInstruction {
		t.Fatalf("system=%q", system)
	}
	if !strings.Contains(content, "Be short") || !strings.Contains(content, "hello") ||
		!strings.Contains(content, "BEGIN SOURCE") {
		t.Fatalf("content=%q", content)
	}
	if r.Text != "summary" || r.Usage.InputTokens != 8 || r.Usage.OutputTokens != 2 {
		t.Fatalf("result=%#v", r)
	}
}

func TestCodexCLIAdapter(t *testing.T) {
	bin := fakeBin(t, "codex", `#!/bin/sh
case "$*" in *"exec"*"--ephemeral"*"--ignore-user-config"*"--ignore-rules"*"--sandbox read-only"*"--json"*) ;; *) echo "bad args: $*" >&2; exit 2;; esac
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"from codex"}}' '{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":3}}'
`)
	t.Setenv("PATH", bin)
	r, err := runCodex(context.Background(), connection{ID: "codex", Adapter: "codex"}, shared.Request{Input: []shared.Part{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "from codex" || r.Usage.InputTokens != 11 || r.Usage.OutputTokens != 3 {
		t.Fatalf("result=%#v", r)
	}
}

func TestClaudeCLIAdapterUsesCompatibleSafeProfile(t *testing.T) {
	bin := fakeBin(t, "claude", `#!/bin/sh
case "$*" in *"--safe-mode"*"--strict-mcp-config"*"--tools "*"--permission-mode dontAsk"*"--no-session-persistence"*"--output-format json"*"-p"*) ;; *) echo "bad args: $*" >&2; exit 2;; esac
printf '%s\n' '{"result":"from claude","usage":{"input_tokens":7,"output_tokens":2}}'
`)
	t.Setenv("PATH", bin)
	r, err := runClaude(context.Background(), connection{ID: "claude", Adapter: "claude"}, shared.Request{Input: []shared.Part{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "from claude" || r.Usage.InputTokens != 7 || r.Usage.OutputTokens != 2 {
		t.Fatalf("result=%#v", r)
	}
}

func fakeBin(t *testing.T, name, body string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, name), []byte(strings.TrimSpace(body)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOpenAIAuthErrorIsClassified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer ts.Close()
	_, err := runOpenAI(context.Background(), connection{BaseURL: ts.URL, Model: "m"}, shared.Request{Input: []shared.Part{{Type: "text", Text: "x"}}})
	pe, ok := err.(providerError)
	if !ok || pe.code != "auth" {
		t.Fatalf("err=%#v", err)
	}
}
