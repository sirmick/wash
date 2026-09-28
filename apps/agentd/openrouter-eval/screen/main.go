// Command model-screen runs one benchmark project against each of several
// OpenRouter models, each alone in a fresh OpenCode session over ACP, and
// scores the result: held-back tests, rule-keeping, speed, how the model
// separates its thinking from its reply, tool calls and cost.
//
//	go run ./apps/agentd/openrouter-eval/screen -models z-ai/glm-5.3,minimax/minimax-m3
//
// It uses the OpenRouter key Wash stores (keys.json), runs every session in
// a new directory under -out with OpenCode's config and data isolated there,
// and stops before a model when the spend so far has reached -budget.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentpolicy"
)

type result struct {
	Model   string  `json:"model"`
	Adapter string  `json:"adapter"`
	Effort  string  `json:"effort"`
	Error   string  `json:"error,omitempty"`
	Stop    string  `json:"stop_reason,omitempty"`
	WallS   float64 `json:"wall_s"`
	FirstS  float64 `json:"first_output_s"`
	AnswerS float64 `json:"first_reply_s"`
	// Hidden is held-back subtests passed out of HiddenOf; the untouched
	// stubs already pass some (the benchmark's README says how many).
	Hidden      int      `json:"hidden_pass"`
	HiddenOf    int      `json:"hidden_of"`
	VisibleOK   bool     `json:"visible_tests_pass"`
	TestsEdited []string `json:"tests_edited,omitempty"`
	ChangesOK   bool     `json:"changes_md_ok"`
	ThoughtCh   int      `json:"thought_chars"`
	ReplyCh     int      `json:"reply_chars"`
	// Leaks are reply lines that read as reasoning (think tags, "Wait —",
	// "the user wants…"), and CodeLines the reply lines inside code fences:
	// a model that drafts code in its reply rather than in an edit is
	// thinking out loud. Both are thinking that arrived as the reply.
	Leaks       int            `json:"reply_leaks"`
	CodeLines   int            `json:"reply_code_lines"`
	Tools       map[string]int `json:"tool_calls"`
	ToolsFailed int            `json:"tool_calls_failed"`
	Asks        int            `json:"permission_asks"`
	Context     int64          `json:"context_used"`
	// Cost is OpenCode's own account of the session's spend; the key's
	// usage lags too much to split between models, so only the run total
	// is taken from it.
	Cost float64 `json:"cost_usd"`
}

func main() {
	models := flag.String("models", "", "comma-separated OpenRouter model ids (without the openrouter/ prefix)")
	effort := flag.String("effort", "high", "OpenCode effort for every model, when the model offers it")
	bench := flag.String("bench", "apps/agentd/openrouter-eval/bench/easy", "benchmark directory: project/, hidden/")
	out := flag.String("out", "", "output directory (default: a new /tmp/wash-screen-*)")
	timeout := flag.Duration("timeout", 10*time.Minute, "limit per model")
	budget := flag.Float64("budget", 5, "stop before a model once this many USD are spent")
	prompt := flag.String("prompt", "Read TASK.md and do what it says.", "the one prompt each model gets")
	config := flag.String("opencode-config", `{"permission":{"edit":"allow","bash":"allow","external_directory":"ask"}}`, "OPENCODE_CONFIG_CONTENT for every session")
	flag.Parse()
	if *models == "" {
		fmt.Fprintln(os.Stderr, "model-screen: -models is required")
		os.Exit(2)
	}
	key := agentpolicy.LoadKeys(agentpolicy.KeysPath())["openrouter"]
	if key == "" {
		fmt.Fprintln(os.Stderr, "model-screen: no openrouter key in", agentpolicy.KeysPath())
		os.Exit(2)
	}
	benchAbs, _ := filepath.Abs(*bench)
	if *out == "" {
		*out, _ = os.MkdirTemp("", "wash-screen-")
	}
	fmt.Println("output:", *out)
	start, _ := keyUsage(key)
	var results []result
	spent := 0.0 // OpenCode's figures; the key's usage lags behind them
	for _, m := range strings.Split(*models, ",") {
		m = strings.TrimSpace(m)
		if now, err := keyUsage(key); err == nil && now-start > spent {
			spent = now - start
		}
		if spent >= *budget {
			fmt.Printf("budget reached ($%.3f); skipping %s and the rest\n", spent, m)
			break
		}
		fmt.Printf("== %s\n", m)
		r := screen(key, m, *effort, *prompt, *config, benchAbs, filepath.Join(*out, strings.ReplaceAll(m, "/", "_")), *timeout)
		results = append(results, r)
		spent += r.Cost
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	_ = os.WriteFile(filepath.Join(*out, "results.json"), b, 0o644)
	table := summary(results)
	_ = os.WriteFile(filepath.Join(*out, "summary.md"), []byte(table), 0o644)
	fmt.Print(table)
	if end, err := keyUsage(key); err == nil {
		fmt.Printf("\nspent this run: $%.4f (key usage, may lag a few seconds)\n", end-start)
	}
}

// keyUsage is the key's total spend so far, in USD.
func keyUsage(key string) (float64, error) {
	req, _ := http.NewRequest("GET", "https://openrouter.ai/api/v1/key", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var v struct {
		Data struct {
			Usage float64 `json:"usage"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return 0, err
	}
	return v.Data.Usage, nil
}

// recorder keeps every update, timestamped, and the running tallies.
type recorder struct {
	mu      sync.Mutex
	t0      time.Time
	log     io.Writer
	r       *result
	tools   map[string]string // id -> last status
	kinds   map[string]string
	reply   strings.Builder
	last    string // kind of the last text chunk, to label the transcript
	script  strings.Builder
	started bool
	work    string
}

var leakRe = regexp.MustCompile(`(?i)</?think(ing)?>|<\|?reasoning|^\s*(okay|ok|alright|hmm)[,.]? (so |let me|the user|i need)|\bthe user (wants|asks|is asking)\b|^\s*(wait|hmm|actually)\b[ ,.—-]|\blet me (re)?(think|consider|restructure|reconsider)\b|\bon second thought\b`)

func (h *recorder) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	h.mu.Lock()
	h.r.Asks++
	fmt.Fprintf(h.log, `{"t":%.3f,"ask":%q}`+"\n", time.Since(h.t0).Seconds(), req.ToolCall.Title)
	h.mu.Unlock()
	// OpenCode asks only about paths outside the project. Its own scratch
	// directory is fine (models check their work there, and a refusal ends
	// OpenCode's turn); anything else is refused.
	want := "reject_once"
	if confined(string(req.ToolCall.RawInput)+" "+req.ToolCall.Title, h.work) {
		want = "allow_once"
	}
	for _, o := range req.Options {
		if o.Kind == want {
			return acp.Selected(o.OptionID), nil
		}
	}
	return acp.Cancelled(), nil
}

var absPath = regexp.MustCompile(`(?:^|[\s'"=:(])(/[^\s'"\\;&|)]+)`)

// confined reports whether every absolute path in s is under the project or
// OpenCode's scratch directory.
func confined(s, work string) bool {
	for _, m := range absPath.FindAllStringSubmatch(s, -1) {
		p := filepath.Clean(m[1])
		if p != work && !strings.HasPrefix(p, work+"/") && p != "/tmp/opencode" && !strings.HasPrefix(p, "/tmp/opencode/") && p != "/dev/null" {
			return false
		}
	}
	return true
}

func (h *recorder) SessionUpdate(_ context.Context, n acp.SessionNotification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u := n.Update
	at := time.Since(h.t0).Seconds()
	fmt.Fprintf(h.log, `{"t":%.3f,"u":%s}`+"\n", at, u.Raw)
	if !h.started {
		return
	}
	text := u.Content.String()
	switch u.SessionUpdate {
	case acp.UpdateAgentThoughtChunk, acp.UpdateAgentMessageChunk:
		if h.r.FirstS == 0 && text != "" {
			h.r.FirstS = at
		}
		label := "thought"
		if u.SessionUpdate == acp.UpdateAgentMessageChunk {
			label = "reply"
			h.r.ReplyCh += len(text)
			h.reply.WriteString(text)
			if h.r.AnswerS == 0 && strings.TrimSpace(text) != "" {
				h.r.AnswerS = at
			}
		} else {
			h.r.ThoughtCh += len(text)
		}
		if label != h.last {
			fmt.Fprintf(&h.script, "\n\n### %s (%.1fs)\n\n", label, at)
			h.last = label
		}
		h.script.WriteString(text)
	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		id := u.ToolCallID
		if u.Kind != "" {
			h.kinds[id] = u.Kind
		}
		if u.SessionUpdate == acp.UpdateToolCall {
			fmt.Fprintf(&h.script, "\n\n### tool %s (%.1fs): %s\n", u.Kind, at, u.Title)
			h.last = "tool"
		}
		if u.Status != "" {
			h.tools[id] = u.Status
		} else if _, ok := h.tools[id]; !ok {
			h.tools[id] = acp.ToolStatusPending
		}
	case acp.UpdateUsage:
		// Raw is the whole notification; the usage is under update.
		var w struct {
			Update struct {
				Used int64 `json:"used"`
				Cost struct {
					Amount float64 `json:"amount"`
				} `json:"cost"`
			} `json:"update"`
		}
		_ = json.Unmarshal(u.Raw, &w)
		v := w.Update
		h.r.Context = v.Used
		if v.Cost.Amount > 0 {
			h.r.Cost = v.Cost.Amount
		}
	}
}

func screen(key, model, effort, prompt, config, bench, dir string, timeout time.Duration) (r result) {
	r = result{Model: model, Effort: effort, Tools: map[string]int{}}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.Error = err.Error()
		return r
	}
	if err := run(dir, "cp", "-a", filepath.Join(bench, "project"), work); err != nil {
		r.Error = err.Error()
		return r
	}
	_ = run(work, "git", "init", "-q")
	_ = run(work, "git", "add", "-A")
	_ = run(work, "git", "-c", "user.name=bench", "-c", "user.email=bench@localhost", "commit", "-qm", "start")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.Command("opencode", "acp")
	cmd.Dir = work
	home := filepath.Join(dir, "home")
	cmd.Env = append(os.Environ(),
		"OPENROUTER_API_KEY="+key,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"OPENCODE_CONFIG_CONTENT="+config,
	)
	stderr, _ := os.Create(filepath.Join(dir, "opencode.stderr"))
	defer stderr.Close()
	cmd.Stderr = stderr
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		r.Error = err.Error()
		return r
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	logf, _ := os.Create(filepath.Join(dir, "events.jsonl"))
	defer logf.Close()
	h := &recorder{t0: time.Now(), work: work, log: logf, r: &r, tools: map[string]string{}, kinds: map[string]string{}}
	c := acp.NewClient(stdout, stdin, h)
	fail := func(err error) result {
		r.Error = err.Error()
		return r
	}
	init, err := c.Initialize(ctx, acp.ClientCapabilities{}, acp.Implementation{Name: "wash-model-screen", Version: "1"})
	if err != nil {
		return fail(err)
	}
	r.Adapter = init.AgentInfo.Name + " " + init.AgentInfo.Version
	s, err := c.NewSession(ctx, work, nil)
	if err != nil {
		return fail(err)
	}
	opts := s.ConfigOptions
	set := func(category, value string) error {
		for _, o := range opts {
			if o.Category != category {
				continue
			}
			if len(o.Options) > 0 && !slices.ContainsFunc(o.Options, func(v acp.ConfigOptionValue) bool { return v.Value == value }) {
				var offered []string
				for _, v := range o.Options {
					offered = append(offered, v.Value)
				}
				return fmt.Errorf("%s: %q not offered (%s)", o.ID, value, strings.Join(offered, ", "))
			}
			res, err := c.SetConfigOption(ctx, s.SessionID, o.ID, value)
			if err != nil {
				return err
			}
			opts = res.ConfigOptions
			return nil
		}
		return fmt.Errorf("no %s option", category)
	}
	if err := set("model", "openrouter/"+model); err != nil {
		return fail(err)
	}
	if err := set("thought_level", effort); err != nil {
		r.Effort = "adapter default (" + err.Error() + ")"
	}

	h.mu.Lock()
	h.t0 = time.Now()
	h.started = true
	h.mu.Unlock()
	pr, err := c.Prompt(ctx, s.SessionID, acp.Text(prompt))
	r.WallS = time.Since(h.t0).Seconds()
	if err != nil {
		r.Error = err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.Error = "timed out after " + timeout.String()
		}
	}
	r.Stop = pr.StopReason
	time.Sleep(3 * time.Second) // let trailing usage updates land
	h.mu.Lock()
	for id, st := range h.tools {
		r.Tools[h.kinds[id]]++
		if st == acp.ToolStatusFailed {
			r.ToolsFailed++
		}
	}
	fenced := false
	for _, line := range strings.Split(h.reply.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			r.CodeLines++
		} else if leakRe.MatchString(line) {
			r.Leaks++
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "transcript.md"), []byte(h.script.String()), 0o644)
	h.mu.Unlock()

	score(bench, work, &r)
	return r
}

func score(bench, work string, r *result) {
	r.VisibleOK = run(work, "go", "test", "./...") == nil
	var diff bytes.Buffer
	cmd := exec.Command("git", "diff", "--name-only", "HEAD", "--", "*_test.go")
	cmd.Dir = work
	cmd.Stdout = &diff
	_ = cmd.Run()
	r.TestsEdited = strings.Fields(diff.String())
	if b, err := os.ReadFile(filepath.Join(work, "CHANGES.md")); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		r.ChangesOK = len(lines) == 3
		for i, l := range lines {
			if !strings.HasPrefix(strings.TrimSpace(l), fmt.Sprintf("%d:", i+1)) {
				r.ChangesOK = false
			}
		}
	}
	hidden, _ := filepath.Glob(filepath.Join(bench, "hidden", "*.go"))
	for _, f := range hidden {
		_ = run(work, "cp", f, work)
	}
	var out bytes.Buffer
	cmd = exec.Command("go", "test", "-json", "-run", "Hidden", "./...")
	cmd.Dir = work
	cmd.Stdout = &out
	_ = cmd.Run()
	for _, line := range strings.Split(out.String(), "\n") {
		var ev struct{ Action, Test string }
		if json.Unmarshal([]byte(line), &ev) != nil || !strings.Contains(ev.Test, "/") {
			continue
		}
		switch ev.Action {
		case "pass":
			r.Hidden++
			r.HiddenOf++
		case "fail":
			r.HiddenOf++
		}
	}
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

func summary(rs []result) string {
	var b strings.Builder
	b.WriteString("\n| model | hidden | visible | rules | wall s | first out s | first reply s | thought/reply chars | leaks/code lines | tools (failed) | asks | context | $ | error |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		tools := 0
		for _, n := range r.Tools {
			tools += n
		}
		rules := "ok"
		if len(r.TestsEdited) > 0 {
			rules = "edited tests"
		} else if !r.ChangesOK {
			rules = "no CHANGES.md"
		}
		fmt.Fprintf(&b, "| %s | %d/%d | %v | %s | %.0f | %.1f | %.1f | %d/%d | %d/%d | %d (%d) | %d | %d | %.4f | %s |\n",
			r.Model, r.Hidden, r.HiddenOf, r.VisibleOK, rules, r.WallS, r.FirstS, r.AnswerS, r.ThoughtCh, r.ReplyCh, r.Leaks, r.CodeLines, tools, r.ToolsFailed, r.Asks, r.Context, r.Cost, r.Error)
	}
	return b.String()
}
