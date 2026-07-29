package agenthook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mapping the whole feature rests on: hook event in, OSC 7770 out.
func TestStatusSequence(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			"session start",
			`{"hook_event_name":"SessionStart","session_id":"s-1","cwd":"/home/mick/wash","source":"startup"}`,
			"\x1b]7770;v=1;ev=start;agent=claude;session=s-1;cwd=/home/mick/wash\x07",
		},
		{
			"user prompt submit is a turn starting",
			`{"hook_event_name":"UserPromptSubmit","session_id":"s-1","prompt":"do the thing"}`,
			"\x1b]7770;v=1;ev=working;agent=claude;session=s-1\x07",
		},
		{
			"permission prompt",
			`{"hook_event_name":"Notification","session_id":"s-1","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`,
			"\x1b]7770;v=1;ev=needs-input;agent=claude;reason=permission;session=s-1\x07",
		},
		{
			"idle prompt",
			`{"hook_event_name":"Notification","session_id":"s-1","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`,
			"\x1b]7770;v=1;ev=needs-input;agent=claude;reason=idle;session=s-1\x07",
		},
		{
			"notification with no type falls back to the message",
			`{"hook_event_name":"Notification","session_id":"s-1","message":"Claude is waiting for your input"}`,
			"\x1b]7770;v=1;ev=needs-input;agent=claude;reason=idle;session=s-1\x07",
		},
		{
			"unknown notification type is still needs-input, unlabelled",
			`{"hook_event_name":"Notification","session_id":"s-1","notification_type":"something_new"}`,
			"\x1b]7770;v=1;ev=needs-input;agent=claude;session=s-1\x07",
		},
		{
			"stop",
			`{"hook_event_name":"Stop","session_id":"s-1","permission_mode":"acceptEdits"}`,
			"\x1b]7770;v=1;ev=done;agent=claude;session=s-1;mode=acceptEdits\x07",
		},
		{
			"session end",
			`{"hook_event_name":"SessionEnd","session_id":"s-1","reason":"clear"}`,
			"\x1b]7770;v=1;ev=end;agent=claude;session=s-1\x07",
		},
		{
			"cwd with spaces is encoded",
			`{"hook_event_name":"SessionStart","cwd":"/home/mick/my projects;odd"}`,
			"\x1b]7770;v=1;ev=start;agent=claude;cwd=/home/mick/my%20projects%3Bodd\x07",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := StatusSequence([]byte(c.payload), AgentClaude)
			if !ok {
				t.Fatalf("StatusSequence(%s) reported no event", c.payload)
			}
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// Events wash has no meaning for (and malformed input) must produce
// nothing at all — silence is the correct behaviour for a hook helper.
func TestStatusSequenceIgnored(t *testing.T) {
	cases := []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
		`{"hook_event_name":"PostToolUse"}`,
		`{"hook_event_name":"SomeFutureEvent"}`,
		`{"session_id":"s-1"}`,
		`not json at all`,
		``,
		`[]`,
	}
	for _, c := range cases {
		if got, ok := StatusSequence([]byte(c), AgentClaude); ok {
			t.Errorf("StatusSequence(%s) emitted %q, want nothing", c, got)
		}
	}
}

// A long cwd must not push the sequence past the parser's 1 KiB bound —
// the terminal side drops an over-long sequence whole, which would mean
// silently losing the event.
func TestStatusSequenceBounded(t *testing.T) {
	long := strings.Repeat("/verylongdirectoryname", 200)
	payload := `{"hook_event_name":"SessionStart","session_id":"` + long + `","cwd":"` + long + `"}`
	got, ok := StatusSequence([]byte(payload), AgentClaude)
	if !ok {
		t.Fatal("no event")
	}
	if len(got) >= 1024 {
		t.Errorf("sequence is %d bytes, must stay under the 1 KiB parser cap", len(got))
	}
}

// End to end through the process entry point: JSON on stdin, an OSC
// sequence on the "tty".
func TestRunStatusWritesToTTY(t *testing.T) {
	dir := t.TempDir()
	ttyPath := filepath.Join(dir, "tty")
	if err := os.WriteFile(ttyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"hook_event_name":"Stop","session_id":"s-9"}`)
	if rc := runStatus([]string{"--tty=" + ttyPath}, in); rc != 0 {
		t.Fatalf("exit %d, want 0", rc)
	}
	data, err := os.ReadFile(ttyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ev=done") {
		t.Errorf("tty got %q, want an ev=done sequence", data)
	}
}

// The helper runs inside someone else's turn: no tty, an unreadable one,
// or an event it doesn't care about must all still exit 0.
func TestRunStatusNeverFails(t *testing.T) {
	cases := []struct {
		name string
		args []string
		in   string
	}{
		{"missing tty", []string{"--tty=/nonexistent/dir/tty"}, `{"hook_event_name":"Stop"}`},
		{"uninteresting event", []string{"--tty=/nonexistent/dir/tty"}, `{"hook_event_name":"PreToolUse"}`},
		{"garbage stdin", []string{"--tty=/nonexistent/dir/tty"}, "\x00\x01garbage"},
		{"empty stdin", []string{"--tty=/nonexistent/dir/tty"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rc := runStatus(c.args, strings.NewReader(c.in)); rc != 0 {
				t.Errorf("exit %d, want 0", rc)
			}
		})
	}
}

// --agent lets a future adapter (codex, gemini) reuse the helper.
func TestStatusSequenceAgentOverride(t *testing.T) {
	got, ok := StatusSequence([]byte(`{"hook_event_name":"Stop"}`), "codex")
	if !ok || !strings.Contains(got, "agent=codex") {
		t.Errorf("got %q, want agent=codex", got)
	}
}

func TestPctEncode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/mick/wash", "/home/mick/wash"},
		{"a b", "a%20b"},
		{"a;b=c", "a%3Bb%3Dc"},
		{"100%", "100%25"},
		{"sess-1_2.3~4", "sess-1_2.3~4"},
		{"user@host:/path", "user@host:/path"},
		{"line\nbreak", "line%0Abreak"},
	}
	for _, c := range cases {
		if got := pctEncode(c.in); got != c.want {
			t.Errorf("pctEncode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
