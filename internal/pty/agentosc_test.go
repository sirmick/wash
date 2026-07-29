package pty

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// scanAll feeds the whole input as one chunk and collects what came out.
func scanAll(t *testing.T, in string) []AgentEvent {
	t.Helper()
	var got []AgentEvent
	s := &agentOSCScanner{}
	s.Feed([]byte(in), func(ev AgentEvent) { got = append(got, ev) })
	return got
}

// scanChunked feeds the input one byte at a time — the torn-sequence
// case, in its most brutal form: every read boundary lands inside the
// escape sequence.
func scanChunked(t *testing.T, in string, size int) []AgentEvent {
	t.Helper()
	var got []AgentEvent
	s := &agentOSCScanner{}
	b := []byte(in)
	for i := 0; i < len(b); i += size {
		end := i + size
		if end > len(b) {
			end = len(b)
		}
		s.Feed(b[i:end], func(ev AgentEvent) { got = append(got, ev) })
	}
	return got
}

const bel = "\x07"

func TestAgentOSCBasic(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want AgentEvent
	}{
		{
			"bel terminated",
			"\x1b]7770;v=1;ev=working;agent=claude" + bel,
			AgentEvent{Version: "1", Event: "working", Agent: "claude"},
		},
		{
			"st terminated",
			"\x1b]7770;v=1;ev=done;agent=claude\x1b\\",
			AgentEvent{Version: "1", Event: "done", Agent: "claude"},
		},
		{
			"all keys",
			"\x1b]7770;v=1;ev=needs-input;agent=claude;session=abc-123;reason=permission;turn_ms=4321;cwd=%2Fhome%2Fmick%2Fwash;mode=acceptEdits" + bel,
			AgentEvent{
				Version: "1", Event: "needs-input", Agent: "claude",
				Session: "abc-123", Reason: "permission", TurnMS: 4321,
				Cwd: "/home/mick/wash", Mode: "acceptEdits",
			},
		},
		{
			"spaces around separators tolerated",
			"\x1b]7770 ; v=1 ; ev=start ; agent=codex" + bel,
			AgentEvent{Version: "1", Event: "start", Agent: "codex"},
		},
		{
			"unknown keys ignored, known ones still land",
			"\x1b]7770;v=2;ev=end;agent=gemini;shiny_new_key=whatever;another=1" + bel,
			AgentEvent{Version: "2", Event: "end", Agent: "gemini"},
		},
		{
			"repeated key takes the last value",
			"\x1b]7770;ev=working;agent=aider;agent=claude" + bel,
			AgentEvent{Event: "working", Agent: "claude"},
		},
		{
			"missing version still parses",
			"\x1b]7770;ev=working" + bel,
			AgentEvent{Event: "working"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scanAll(t, c.in)
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1: %+v", len(got), got)
			}
			if got[0] != c.want {
				t.Errorf("got %+v, want %+v", got[0], c.want)
			}
		})
	}
}

func TestAgentOSCIgnored(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"plain text", "hello world\nsecond line\n"},
		{"osc title (id 0)", "\x1b]0;user@host: ~/wash" + bel},
		{"osc 7 cwd", "\x1b]7;file://host/home/mick" + bel},
		{"osc 52 clipboard", "\x1b]52;c;" + strings.Repeat("QUJD", 400) + bel},
		{"id prefix but different (77700)", "\x1b]77700;ev=working" + bel},
		{"id only, no params", "\x1b]7770" + bel},
		{"unknown event", "\x1b]7770;v=1;ev=teleporting;agent=claude" + bel},
		{"missing event", "\x1b]7770;v=1;agent=claude" + bel},
		{"csi sequence, not osc", "\x1b[31mred\x1b[0m"},
		{"unterminated (no BEL/ST)", "\x1b]7770;v=1;ev=working;agent=claude"},
		{"aborted by newline mid-body", "\x1b]7770;v=1;ev=working\nagent=claude" + bel},
		{"empty body", "\x1b]" + bel},
		{"lone escape", "\x1b"},
		{"escape then junk", "\x1bXhello"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scanAll(t, c.in); len(got) != 0 {
				t.Errorf("got %d events, want none: %+v", len(got), got)
			}
		})
	}
}

// The whole point of the scanner: a pty read can end anywhere, including
// between the ESC and the ']', inside a key, or between ESC and '\' of an
// ST terminator. Every chunk size must produce the same events.
func TestAgentOSCTornAcrossChunks(t *testing.T) {
	in := "before\x1b]7770;v=1;ev=working;agent=claude;session=s1" + bel +
		"middle\x1b]0;title" + bel +
		"\x1b]7770;v=1;ev=needs-input;reason=permission\x1b\\after"
	want := []AgentEvent{
		{Version: "1", Event: "working", Agent: "claude", Session: "s1"},
		{Version: "1", Event: "needs-input", Reason: "permission"},
	}
	for size := 1; size <= len(in); size++ {
		got := scanChunked(t, in, size)
		if len(got) != len(want) {
			t.Fatalf("chunk=%d: got %d events, want %d: %+v", size, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("chunk=%d: event %d = %+v, want %+v", size, i, got[i], want[i])
			}
		}
	}
}

// Back-to-back events in one read, and an event immediately following an
// unrelated OSC — the scanner must not lose the second one.
func TestAgentOSCMultiplePerFeed(t *testing.T) {
	in := "\x1b]7770;ev=start;agent=claude" + bel +
		"\x1b]7770;ev=working;agent=claude" + bel +
		"\x1b]7770;ev=done;agent=claude" + bel
	got := scanAll(t, in)
	want := []string{"start", "working", "done"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, ev := range want {
		if got[i].Event != ev {
			t.Errorf("event %d = %q, want %q", i, got[i].Event, ev)
		}
	}
}

// Bounds: an over-long body is dropped whole, never truncated-and-parsed,
// and the scanner recovers on the next well-formed sequence. Feeding the
// oversize body in small chunks also proves the accumulator can't grow
// past the cap while a torn sequence is in flight.
func TestAgentOSCOverflowDropsAndRecovers(t *testing.T) {
	huge := "\x1b]7770;v=1;ev=working;agent=claude;cwd=" + strings.Repeat("A", agentOSCMax*4) + bel
	good := "\x1b]7770;v=1;ev=done;agent=claude" + bel
	for _, size := range []int{1, 7, 64, 1 << 16} {
		var got []AgentEvent
		s := &agentOSCScanner{}
		b := []byte(huge + good)
		for i := 0; i < len(b); i += size {
			end := i + size
			if end > len(b) {
				end = len(b)
			}
			s.Feed(b[i:end], func(ev AgentEvent) { got = append(got, ev) })
			if len(s.buf) > agentOSCMax {
				t.Fatalf("chunk=%d: accumulator grew to %d, cap is %d", size, len(s.buf), agentOSCMax)
			}
		}
		if len(got) != 1 || got[0].Event != "done" {
			t.Fatalf("chunk=%d: got %+v, want just the trailing done event", size, got)
		}
	}
}

// A body that is exactly at the cap is still rejected (the cap is the
// accumulator's limit, not a suggestion); one comfortably under it parses.
func TestAgentOSCLengthBoundary(t *testing.T) {
	pad := func(n int) string {
		return "\x1b]7770;ev=working;cwd=" + strings.Repeat("x", n) + bel
	}
	// Body length = len("7770;ev=working;cwd=") + n.
	const head = len("7770;ev=working;cwd=")
	if got := scanAll(t, pad(agentOSCMax-head-1)); len(got) != 1 {
		t.Errorf("just under the cap: got %d events, want 1", len(got))
	}
	if got := scanAll(t, pad(agentOSCMax)); len(got) != 0 {
		t.Errorf("over the cap: got %d events, want 0", len(got))
	}
}

func TestAgentOSCPercentDecode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"%2Fhome%2Fmick", "/home/mick"},
		{"a%20b", "a b"},
		{"%3B%3D%25", ";=%"},
		{"%2f%6C%6fwer", "/lower"},
		{"nothing", "nothing"},
		{"trailing%", "trailing%"},
		{"bad%zz", "bad%zz"},
		{"short%2", "short%2"},
		{"%%41", "%A"},
	}
	for _, c := range cases {
		if got := pctDecode(c.in); got != c.want {
			t.Errorf("pctDecode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Values are display text that reaches logs and FE chrome, so a decoded
// control character must never survive — otherwise a pty-resident process
// could forge a log line or smuggle an escape sequence into a tab label.
func TestAgentOSCValueSanitized(t *testing.T) {
	got := scanAll(t, "\x1b]7770;ev=working;agent=cla%0Aude%1b[31m;cwd=%00%7Ftmp"+bel)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if strings.ContainsAny(got[0].Agent, "\n\x1b") {
		t.Errorf("agent %q still carries control characters", got[0].Agent)
	}
	if strings.ContainsAny(got[0].Cwd, "\x00\x7f") {
		t.Errorf("cwd %q still carries control characters", got[0].Cwd)
	}
	long := strings.Repeat("z", agentValueMax*2)
	got = scanAll(t, "\x1b]7770;ev=working;session="+long+bel)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if len(got[0].Session) > agentValueMax {
		t.Errorf("session value %d bytes, cap is %d", len(got[0].Session), agentValueMax)
	}
}

func TestAgentOSCTurnMS(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"turn_ms=0", 0},
		{"turn_ms=43000", 43000},
		{"turn_ms=-5", 0},
		{"turn_ms=abc", 0},
		{"turn_ms=", 0},
	}
	for _, c := range cases {
		got := scanAll(t, "\x1b]7770;ev=done;"+c.in+bel)
		if len(got) != 1 {
			t.Fatalf("%s: got %d events, want 1", c.in, len(got))
		}
		if got[0].TurnMS != c.want {
			t.Errorf("%s: TurnMS = %d, want %d", c.in, got[0].TurnMS, c.want)
		}
	}
}

// The sequence must survive untouched: the scanner reads, it never
// rewrites (§3 — the ring stays byte-exact and xterm.js ignores the id).
func TestAgentOSCDoesNotModifyInput(t *testing.T) {
	in := []byte("out\x1b]7770;v=1;ev=working;agent=claude" + bel + "more")
	orig := append([]byte(nil), in...)
	s := &agentOSCScanner{}
	s.Feed(in, func(AgentEvent) {})
	if !bytes.Equal(in, orig) {
		t.Errorf("Feed modified its input: %q", in)
	}
}

// Fuzz the accumulator: no input may panic the scanner or leave it
// holding more than the cap.
func FuzzAgentOSCScanner(f *testing.F) {
	f.Add("\x1b]7770;v=1;ev=working;agent=claude\x07")
	f.Add("\x1b]7770;ev=needs-input;reason=permission\x1b\\")
	f.Add("\x1b]0;title\x07\x1b]7770;ev=done\x07")
	f.Add("\x1b\x1b]]7770;;;==;ev=\x07")
	f.Add(strings.Repeat("\x1b]7770;", 200))
	f.Fuzz(func(t *testing.T, in string) {
		s := &agentOSCScanner{}
		for i := 0; i < len(in); i += 3 {
			end := i + 3
			if end > len(in) {
				end = len(in)
			}
			s.Feed([]byte(in[i:end]), func(ev AgentEvent) {
				if !agentEvents[ev.Event] {
					t.Fatalf("emitted unknown event %q", ev.Event)
				}
			})
			if len(s.buf) > agentOSCMax {
				t.Fatalf("accumulator %d > cap %d", len(s.buf), agentOSCMax)
			}
		}
	})
}

func TestMatchAgent(t *testing.T) {
	cases := []struct {
		name string
		comm string
		argv []string
		want string
	}{
		{"claude native", "claude", []string{"claude"}, "claude"},
		{"codex", "codex", []string{"codex", "exec"}, "codex"},
		{"gemini", "gemini", nil, "gemini"},
		{"aider", "aider", nil, "aider"},
		{"amp", "amp", nil, "amp"},
		{"cursor-agent slug differs", "cursor-agent", nil, "cursor"},
		{"node running claude js", "node", []string{"node", "/usr/lib/node_modules/claude.js"}, "claude"},
		{"node with flags first", "node", []string{"node", "--enable-source-maps", "/home/m/.local/bin/claude"}, "claude"},
		{"bun", "bun", []string{"bun", "/opt/aider"}, "aider"},
		// The negative case the whole T0 tier is judged on.
		{"vi is not an agent", "vi", []string{"vi", "notes.md"}, ""},
		{"vim is not an agent", "vim", nil, ""},
		{"bash is not an agent", "bash", []string{"bash"}, ""},
		{"ssh is not an agent", "ssh", []string{"ssh", "files"}, ""},
		{"node running something else", "node", []string{"node", "server.js"}, ""},
		{"argv only matters for runtimes", "make", []string{"make", "claude"}, ""},
		{"argv[0] alone never matches", "node", []string{"claude"}, ""},
		{"flags are skipped, not matched", "node", []string{"node", "--claude"}, ""},
		{"empty", "", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchAgent(c.comm, c.argv); got != c.want {
				t.Errorf("matchAgent(%q, %q) = %q, want %q", c.comm, c.argv, got, c.want)
			}
		})
	}
}

// Belt and braces on the FE-facing contract: the emitted event names are
// exactly the five the FE knows how to render.
func TestAgentEventNames(t *testing.T) {
	want := []string{"start", "working", "needs-input", "done", "end"}
	if len(agentEvents) != len(want) {
		t.Fatalf("agentEvents has %d entries, want %d", len(agentEvents), len(want))
	}
	for _, ev := range want {
		if !agentEvents[ev] {
			t.Errorf("missing event %q", ev)
		}
		in := fmt.Sprintf("\x1b]7770;v=1;ev=%s%s", ev, bel)
		if got := scanAll(t, in); len(got) != 1 || got[0].Event != ev {
			t.Errorf("event %q did not round-trip: %+v", ev, got)
		}
	}
}
