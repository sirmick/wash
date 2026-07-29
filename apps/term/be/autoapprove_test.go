package term

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Every "false" in this table is a keystroke wash did NOT send into
// someone's terminal. They are the point of the test.
func TestMatchAutoApprove(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want bool
	}{
		// The shapes a hookless agent actually prints.
		{"y/n question", "Apply this change? (y/n) ", true},
		{"y/n colon", "Proceed? (y/n):", true},
		{"bracket form", "Overwrite file [y/n] ", true},
		{"yes/no", "Continue? (yes/no)", true},
		{"aider", "Apply edit to main.go? (Y)es/(N)o [Yes]:", true},
		{"aider with skip", "Add file? (Y)es/(N)o/(A)ll/(S)kip all [Yes]:", true},
		{"trailing newline still a prompt", "Proceed? (y/n)\n", true},
		{"coloured prompt", "\x1b[1;33mProceed?\x1b[0m \x1b[32m(y/n)\x1b[0m", true},

		// …and everything that merely mentions one.
		{"question already answered", "Proceed? (y/n) y\nApplied.\n", false},
		{"prose about the flag", "pass (y/n) to skip the prompt, then continue\n", false},
		{"source code being catted", `if ask("(y/n)") { doIt() }`, false},
		{"grep output", "main.go:42:  fmt.Print(\"(y/n) \")\nmain.go:88:  ...\n", false},
		{"shell prompt", "mick@buzz:~/wash$ ", false},
		{"empty", "", false},
		{"whitespace", "   \n\t ", false},
		{"partial", "Proceed? (y/", false},
		{"different question", "Password:", false},
		{"claude code menu", "Do you want to proceed?\n1. Yes\n2. No", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, got := matchAutoApprove(stripANSI(c.tail))
			if got != c.want {
				t.Errorf("matchAutoApprove(%q) = %v, want %v", c.tail, got, c.want)
			}
		})
	}
}

func TestAutoApproveReplies(t *testing.T) {
	cases := map[string]string{
		"Proceed? (y/n) ":                       "y\n",
		"Continue? (yes/no)":                    "yes\n",
		"Apply edit to x.go? (Y)es/(N)o [Yes]:": "y\n",
	}
	for tail, want := range cases {
		_, reply, ok := matchAutoApprove(tail)
		if !ok || reply != want {
			t.Errorf("matchAutoApprove(%q) = (%q, %v), want %q", tail, reply, ok, want)
		}
	}
}

// The prompt can arrive split across pty reads — that's the normal case,
// not the exception.
func TestAutoApproveAcrossChunks(t *testing.T) {
	var s autoApproveState
	now := time.Now()
	if _, reply := s.feed([]byte("Apply this change? ("), now); reply != "" {
		t.Fatalf("fired on a partial prompt: %q", reply)
	}
	_, reply := s.feed([]byte("y/n) "), now)
	if reply != "y\n" {
		t.Errorf("reply = %q, want y", reply)
	}
}

// One answer per question, and never a burst.
func TestAutoApproveRateLimitAndDedupe(t *testing.T) {
	var s autoApproveState
	now := time.Now()
	if _, reply := s.feed([]byte("Proceed? (y/n) "), now); reply == "" {
		t.Fatal("first prompt not answered")
	}
	// The same unanswered question re-rendered (a redraw) gets nothing.
	if _, reply := s.feed([]byte(""), now.Add(time.Second)); reply != "" {
		t.Errorf("redraw answered again: %q", reply)
	}
	// A NEW question inside the gap is still suppressed.
	if _, reply := s.feed([]byte("\nAnd another? (y/n) "), now.Add(time.Second)); reply != "" {
		t.Errorf("answered inside the rate-limit gap: %q", reply)
	}
	// Past the gap, a new question is answered.
	if _, reply := s.feed([]byte("\nA third? (y/n) "), now.Add(autoApproveGap+time.Second)); reply == "" {
		t.Error("no answer after the gap elapsed")
	}
}

// The window is bounded: a terminal streaming megabytes must not grow it.
func TestAutoApproveTailBounded(t *testing.T) {
	var s autoApproveState
	now := time.Now()
	chunk := []byte(strings.Repeat("x", 4096))
	for i := 0; i < 50; i++ {
		s.feed(chunk, now)
		if len(s.tail) > autoApproveTail {
			t.Fatalf("tail grew to %d, cap is %d", len(s.tail), autoApproveTail)
		}
	}
	// …and it still recognizes a prompt at the end of the flood.
	if _, reply := s.feed([]byte("Proceed? (y/n) "), now); reply != "y\n" {
		t.Errorf("reply after a flood = %q", reply)
	}
}

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[1;33mbold\x1b[0m", "bold"},
		{"\x1b]0;title\x07after", "after"},
		{"\x1b]0;title\x1b\\after", "after"},
		{"a\x1b[Kb", "ab"},
		{"trailing\x1b", "trailing\x1b"},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The four gates, exercised through the real entry point. Each one alone
// is enough to stop an injection.
func TestOnTabOutputGates(t *testing.T) {
	const chanID = 3
	prompt := []byte("Apply this change? (y/n) ")

	setup := func(t *testing.T, policy string, foregroundAgent string) *[]string {
		t.Helper()
		dir := t.TempDir()
		path := dir + "/agents.json"
		if err := writeFile(path, policy); err != nil {
			t.Fatal(err)
		}
		policyStore = policyCache{path: path}
		initState()
		if foregroundAgent != "" {
			st.agents[chanID] = &agentRec{pollAgent: foregroundAgent}
		}
		var injected []string
		return &injected
	}
	inject := func(injected *[]string) func([]byte) (int, error) {
		return func(p []byte) (int, error) {
			*injected = append(*injected, string(p))
			return len(p), nil
		}
	}

	t.Run("everything on → answers", func(t *testing.T) {
		injected := setup(t, `{"enabled":true,"legacy_autoapprove":true}`, "aider")
		onTabOutput(chanID, inject(injected), nil, prompt)
		if len(*injected) != 1 || (*injected)[0] != "y\n" {
			t.Errorf("injected %q, want one y", *injected)
		}
	})
	t.Run("feature off → silent", func(t *testing.T) {
		injected := setup(t, `{"enabled":true}`, "aider")
		onTabOutput(chanID, inject(injected), nil, prompt)
		if len(*injected) != 0 {
			t.Errorf("injected %q with legacy_autoapprove unset", *injected)
		}
	})
	t.Run("policy kill switch off → silent", func(t *testing.T) {
		injected := setup(t, `{"enabled":false,"legacy_autoapprove":true}`, "aider")
		onTabOutput(chanID, inject(injected), nil, prompt)
		if len(*injected) != 0 {
			t.Errorf("injected %q with the policy disabled", *injected)
		}
	})
	t.Run("no agent in the foreground → silent", func(t *testing.T) {
		// The load-bearing one: a shell, a build, or `cat`ting a file that
		// happens to contain a (y/n) prompt must never be typed into.
		injected := setup(t, `{"enabled":true,"legacy_autoapprove":true}`, "")
		onTabOutput(chanID, inject(injected), nil, prompt)
		if len(*injected) != 0 {
			t.Errorf("injected %q with no agent running", *injected)
		}
	})
	t.Run("no policy file → silent", func(t *testing.T) {
		policyStore = policyCache{path: t.TempDir() + "/missing.json"}
		initState()
		st.agents[chanID] = &agentRec{pollAgent: "aider"}
		var injected []string
		onTabOutput(chanID, inject(&injected), nil, prompt)
		if len(injected) != 0 {
			t.Errorf("injected %q with no policy file", injected)
		}
	})
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
