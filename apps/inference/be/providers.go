package inferenceapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	shared "github.com/sirmick/wash/pkg/inference"
)

const maxProviderOutput = 1 << 20

type providerError struct{ code, msg string }

func (e providerError) Error() string { return e.msg }

func runProvider(ctx context.Context, c connection, req shared.Request) (shared.Result, error) {
	start := time.Now()
	var r shared.Result
	var err error
	switch c.Adapter {
	case "openai":
		r, err = runOpenAI(ctx, c, req)
	case "codex":
		r, err = runCodex(ctx, c, req)
	case "claude":
		r, err = runClaude(ctx, c, req)
	default:
		err = providerError{"not_configured", "unknown provider adapter"}
	}
	r.Provider, r.Model, r.ElapsedMS = c.ID, c.Model, time.Since(start).Milliseconds()
	return r, err
}

func prompt(req shared.Request) string {
	var b strings.Builder
	if req.Instructions != "" {
		b.WriteString(req.Instructions)
		b.WriteString("\n\n")
	}
	for _, p := range req.Input {
		b.WriteString(p.Text)
		if !strings.HasSuffix(p.Text, "\n") {
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

func runOpenAI(ctx context.Context, c connection, req shared.Request) (shared.Result, error) {
	if c.BaseURL == "" || c.Model == "" {
		return shared.Result{}, providerError{"not_configured", "base URL and model are required"}
	}
	max := req.MaxOutputTokens
	if max <= 0 {
		max = 1024
	}
	body := map[string]any{"model": c.Model, "messages": []map[string]string{{"role": "user", "content": prompt(req)}}, "max_tokens": max}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	b, _ := json.Marshal(body)
	u := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return shared.Result{}, providerError{"bad_response", err.Error()}
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(hreq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return shared.Result{}, providerError{"timeout", "provider timed out"}
		}
		return shared.Result{}, providerError{"unavailable", err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderOutput+1))
	if err != nil || len(raw) > maxProviderOutput {
		return shared.Result{}, providerError{"bad_response", "provider response was too large or unreadable"}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return shared.Result{}, providerError{"auth", "provider rejected the credential"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return shared.Result{}, providerError{"unavailable", fmt.Sprintf("provider returned HTTP %d", resp.StatusCode)}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return shared.Result{}, providerError{"bad_response", "provider returned no message text"}
	}
	return shared.Result{Text: out.Choices[0].Message.Content, Usage: shared.Usage{InputTokens: out.Usage.Prompt, OutputTokens: out.Usage.Completion}}, nil
}

func runCodex(ctx context.Context, c connection, req shared.Request) (shared.Result, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return shared.Result{}, providerError{"unavailable", "codex executable not found"}
	}
	dir, err := os.MkdirTemp("", "wash-inference-codex-")
	if err != nil {
		return shared.Result{}, providerError{"internal", err.Error()}
	}
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	args := []string{"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--sandbox", "read-only", "--skip-git-repo-check", "--json"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, "-")
	out, err := command(ctx, dir, prompt(req), "codex", args...)
	if err != nil {
		return shared.Result{}, err
	}
	var text string
	var inTok, outTok int
	s := bufio.NewScanner(bytes.NewReader(out))
	s.Buffer(make([]byte, 64*1024), maxProviderOutput)
	for s.Scan() {
		var ev map[string]any
		if json.Unmarshal(s.Bytes(), &ev) != nil {
			continue
		}
		if item, ok := ev["item"].(map[string]any); ok && item["type"] == "agent_message" {
			if v, ok := item["text"].(string); ok {
				text = v
			}
		}
		if usage, ok := ev["usage"].(map[string]any); ok {
			inTok = int(number(usage["input_tokens"]))
			outTok = int(number(usage["output_tokens"]))
		}
	}
	if text == "" {
		return shared.Result{}, providerError{"bad_response", "codex returned no agent message"}
	}
	return shared.Result{Text: text, Usage: shared.Usage{InputTokens: inTok, OutputTokens: outTok}}, nil
}

func runClaude(ctx context.Context, c connection, req shared.Request) (shared.Result, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return shared.Result{}, providerError{"unavailable", "claude executable not found"}
	}
	dir, err := os.MkdirTemp("", "wash-inference-claude-")
	if err != nil {
		return shared.Result{}, providerError{"internal", err.Error()}
	}
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	// Keep to flags supported by the long-lived -p interface, including older
	// Claude Code builds. Safe mode suppresses CLAUDE.md, hooks, plugins and user
	// MCP configuration; the remaining flags make the no-tools boundary explicit.
	args := []string{"--safe-mode", "--strict-mcp-config", "--tools", "", "--disallowedTools", "mcp__*", "--permission-mode", "dontAsk", "--no-session-persistence", "--output-format", "json"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, "-p", "Respond only to the one-shot input on stdin. Do not perform actions or use tools.")
	out, err := command(ctx, dir, prompt(req), "claude", args...)
	if err != nil {
		return shared.Result{}, err
	}
	var v struct {
		Result string `json:"result"`
		Usage  struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(out, &v) != nil || v.Result == "" {
		return shared.Result{}, providerError{"bad_response", "claude returned no result"}
	}
	return shared.Result{Text: v.Result, Usage: shared.Usage{InputTokens: v.Usage.Input, OutputTokens: v.Usage.Output}}, nil
}

func command(ctx context.Context, dir, stdin, name string, args ...string) ([]byte, error) {
	p, _ := exec.LookPath(name)
	cmd := exec.Command(p, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = minimalEnv(name)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, n: maxProviderOutput}
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	if err := cmd.Start(); err != nil {
		return nil, providerError{"unavailable", err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, providerError{"unavailable", msg}
		}
		return out.Bytes(), nil
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return nil, providerError{"timeout", "provider timed out"}
	}
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.n <= 0 {
		return original, errors.New("output limit exceeded")
	}
	if len(p) > w.n {
		p = p[:w.n]
	}
	n, err := w.w.Write(p)
	w.n -= n
	if n < original {
		return original, errors.New("output limit exceeded")
	}
	return n, err
}
func number(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}
func minimalEnv(provider string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "XDG_CONFIG_HOME": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true}
	if provider == "codex" {
		allowed["CODEX_HOME"] = true
		allowed["OPENAI_API_KEY"] = true
	}
	if provider == "claude" {
		allowed["ANTHROPIC_API_KEY"] = true
		allowed["CLAUDE_CODE_OAUTH_TOKEN"] = true
	}
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if allowed[k] {
			out = append(out, e)
		}
	}
	return out
}

func executableStatus(c connection) (bool, string) {
	if c.Adapter == "openai" {
		return c.BaseURL != "" && c.Model != "", ""
	}
	p, err := exec.LookPath(c.Adapter)
	if err != nil {
		return false, "not found"
	}
	return true, filepath.Base(p)
}
