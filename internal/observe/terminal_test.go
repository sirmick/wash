package observe

import (
	"strings"
	"testing"
)

func TestTerminalStripsColourAndTitleSequences(t *testing.T) {
	raw := []byte("\x1b]0;mick@buzz: ~/wash\x07\x1b[1;32mmick@buzz\x1b[0m:\x1b[34m~/wash\x1b[0m$ ls --color\r\n\x1b[01;34mdocs\x1b[0m  \x1b[01;34minternal\x1b[0m  README.md\r\n")
	got, truncated := Terminal(raw, 0)
	want := "mick@buzz:~/wash$ ls --color\ndocs  internal  README.md"
	if got != want || truncated {
		t.Fatalf("got %q (truncated=%v)\nwant %q", got, truncated, want)
	}
}

func TestTerminalResolvesProgressBarsAndBackspaces(t *testing.T) {
	// A progress bar redraws with \r; a typo is fixed with \b.
	raw := []byte("downloading  10%\rdownloading  55%\rdownloading 100%\ngit sttaus\b\b\b\batus\n")
	got, _ := Terminal(raw, 0)
	if got != "downloading 100%\ngit status" {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalCollapsesBlankRunsAndTrimsEdges(t *testing.T) {
	raw := []byte("\n\n\nfirst\n\n\n\nsecond\n\n\n")
	got, _ := Terminal(raw, 0)
	if got != "first\n\nsecond" {
		t.Fatalf("got %q", got)
	}
}

func TestTerminalTailIsBoundedAndStartsOnALine(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", 20))
		sb.WriteByte('\n')
	}
	got, truncated := Terminal([]byte(sb.String()), 4096)
	if !truncated {
		t.Fatal("a tail longer than max was not marked truncated")
	}
	if len(got) > 4096 || !strings.HasPrefix(got, "line ") {
		t.Fatalf("tail=%d bytes, starts %q", len(got), got[:10])
	}
	if _, over := Terminal([]byte("short"), MaxTailBytes*4); over {
		t.Fatal("an oversized max must fall back to the default, not truncate")
	}
}

func TestTerminalDropsControlsButKeepsUTF8(t *testing.T) {
	raw := []byte("caf\xc3\xa9 \x01\x02 ok \xff end\x1b[?25l\x1b[2J")
	got, _ := Terminal(raw, 0)
	if got != "café  ok \uFFFD end" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactScrubsCredentialShapes(t *testing.T) {
	cases := map[string]string{
		"export OPENAI_API_KEY=sk-proj-abcdefgh12345678":       "export OPENAI_API_KEY=[redacted]",
		"curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9'": "curl -H 'Authorization: Bearer [redacted]'",
		"password: hunter2 and more":                           "password: [redacted] and more",
		`{"path":"/tmp/a.go","password":"hunter2","n":1}`:      `{"path":"/tmp/a.go","password":"[redacted]","n":1}`,
		"aws AKIAIOSFODNN7EXAMPLE used":                        "aws [redacted] used",
		"ghp_abcdefghijklmnop123456 pushed":                    "[redacted] pushed",
		"nothing secret here":                                  "nothing secret here",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
