package login

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProcEntry writes a synthetic /proc/<pid>/ entry under root.
// status and cmdline are written verbatim; cmdline gets a trailing
// NUL like the real kernel produces.
func fakeProcEntry(t *testing.T, root string, pid int, status string, argv []string) {
	t.Helper()
	dir := filepath.Join(root, "0") // start with placeholder, then rename
	_ = dir
	pidDir := filepath.Join(root, intToStr(pid))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pidDir, err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "status"), []byte(status), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	cmdline := strings.Join(argv, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

func TestProcRegistryHappyPath(t *testing.T) {
	root := t.TempDir()
	reg := &ProcRegistry{Root: root}

	// One matching wash-router process owned by uid 1000.
	fakeProcEntry(t, root, 4321,
		"Name:\twash-router\nState:\tS (sleeping)\nUid:\t1000\t1000\t1000\t1000\n",
		[]string{"/usr/bin/wash-router", "--listen-unix", "/run/wash/1000/sessions/s-abc1.sock", "--name", "mick"},
	)
	// A second wash-router owned by uid 1001 — should be filtered out.
	fakeProcEntry(t, root, 5555,
		"State:\tS\nUid:\t1001\t1001\t1001\t1001\n",
		[]string{"/usr/bin/wash-router", "--listen-unix", "/run/wash/1001/sessions/s-def2.sock"},
	)
	// A non-router process — should be filtered out.
	fakeProcEntry(t, root, 6666,
		"State:\tR\nUid:\t1000\t1000\t1000\t1000\n",
		[]string{"/usr/bin/sshd"},
	)
	// A zombie wash-router — should be filtered out.
	fakeProcEntry(t, root, 7777,
		"State:\tZ\nUid:\t1000\t1000\t1000\t1000\n",
		[]string{"/usr/bin/wash-router", "--listen-unix", "/run/wash/1000/sessions/s-zomb.sock"},
	)

	sessions, err := reg.List(1000)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(sessions), sessions)
	}
	s := sessions[0]
	if s.Pid != 4321 || s.UID != 1000 || s.SessID != "s-abc1" || s.Name != "mick" {
		t.Errorf("unexpected session: %+v", s)
	}
	if s.Sock != "/run/wash/1000/sessions/s-abc1.sock" {
		t.Errorf("Sock: got %q want /run/wash/1000/sessions/s-abc1.sock", s.Sock)
	}
}

func TestProcRegistryEmpty(t *testing.T) {
	root := t.TempDir()
	reg := &ProcRegistry{Root: root}
	sessions, err := reg.List(1000)
	if err != nil {
		t.Fatalf("List on empty proc: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected no sessions, got %+v", sessions)
	}
}

func TestProcRegistryEqualsFormFlag(t *testing.T) {
	root := t.TempDir()
	reg := &ProcRegistry{Root: root}
	fakeProcEntry(t, root, 1234,
		"State:\tS\nUid:\t1000\t1000\t1000\t1000\n",
		[]string{
			"/usr/bin/wash-router",
			"--listen-unix=/run/wash/1000/sessions/s-7f2a.sock",
			"--name=research",
		},
	)
	sessions, err := reg.List(1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessID != "s-7f2a" || sessions[0].Name != "research" {
		t.Errorf("--flag=value form not parsed: %+v", sessions)
	}
}

func TestParseStatus(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantUID   uint32
		wantState byte
		wantOK    bool
	}{
		{"happy", "Name:\twash-router\nUid:\t1000\t1000\t1000\t1000\nState:\tS\n", 1000, 'S', true},
		{"missing state", "Uid:\t1000\t1000\t1000\t1000\n", 0, 0, false},
		{"missing uid", "State:\tR\n", 0, 0, false},
		{"empty", "", 0, 0, false},
		{"zombie", "State:\tZ (zombie)\nUid:\t0\t0\t0\t0\n", 0, 'Z', true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid, state, ok := parseStatus([]byte(tc.input))
			if ok != tc.wantOK {
				t.Errorf("ok: got %v want %v", ok, tc.wantOK)
			}
			if ok {
				if uid != tc.wantUID {
					t.Errorf("uid: got %d want %d", uid, tc.wantUID)
				}
				if state != tc.wantState {
					t.Errorf("state: got %c want %c", state, tc.wantState)
				}
			}
		})
	}
}

func TestParseCmdline(t *testing.T) {
	// Real kernel output: NUL-separated argv, trailing NUL.
	input := []byte("/usr/bin/wash-router\x00--listen-unix\x00/path/x.sock\x00")
	got := parseCmdline(input)
	want := []string{"/usr/bin/wash-router", "--listen-unix", "/path/x.sock"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("argv[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestSessIDFromSock(t *testing.T) {
	cases := []struct {
		sock string
		want string
		ok   bool
	}{
		{"/run/wash/1000/sessions/s-abc1.sock", "s-abc1", true},
		{"/tmp/test/x.sock", "x", true},
		{"/no-extension", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := sessIDFromSock(tc.sock, 1000)
		if ok != tc.ok || got != tc.want {
			t.Errorf("sessIDFromSock(%q): got (%q, %v) want (%q, %v)", tc.sock, got, ok, tc.want, tc.ok)
		}
	}
}
