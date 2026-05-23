package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

const (
	// scrollback — how many lines `tail` pre-loads before following.
	scrollback = 500

	// batchInterval — how often we flush accumulated lines to the FE.
	// Matches wash-journal so the two apps feel the same.
	batchInterval = 100 * time.Millisecond

	// flushAt — early flush threshold when a chatty file produces
	// > flushAt lines within one batch window.
	flushAt = 200
)

// LogEntry is the wire shape for one parsed syslog line. Priority is
// best-effort: classic syslog files don't carry it inline (the
// daemon stripped <PRI> before writing), so we fall back to a coarse
// heuristic on the message body. Callers should treat priority<0 as
// "unknown — render neutral".
type LogEntry struct {
	TS       int64  `cbor:"ts" json:"ts"`             // µs since epoch (0 if unparsed)
	Priority int    `cbor:"priority" json:"priority"` // -1 unknown; 0..7 RFC values
	Ident    string `cbor:"ident" json:"ident"`       // program name (sshd, kernel, …)
	PID      int    `cbor:"pid" json:"pid"`           // 0 if absent
	Message  string `cbor:"message" json:"message"`
	Raw      string `cbor:"raw" json:"raw"` // original line — fallback view
}

// streamCtl owns one running tail. Same shape as wash-journal's
// streamCtl; we keep the two implementations side-by-side rather than
// extract into a shared internal package — the bodies have enough
// small differences (no per-priority filter, syslog line parser
// instead of JSON) that a shared abstraction would be premature.
type streamCtl struct {
	conn *sdk.Conn
	gen  int64
	req  selectReq

	ctx       context.Context
	cancelCtx context.CancelFunc

	cmd *exec.Cmd

	privReqID    string
	privStdout   []byte
	privStderr   []byte
	privStdoutMu sync.Mutex
	privStderrMu sync.Mutex

	mu         sync.Mutex
	pending    []LogEntry
	stderrAcc  []byte
	closedOnce sync.Once
	closed     atomic.Bool
}

func newStream(c *sdk.Conn, gen int64, req selectReq) *streamCtl {
	ctx, cancel := context.WithCancel(context.Background())
	return &streamCtl{
		conn:      c,
		gen:       gen,
		req:       req,
		ctx:       ctx,
		cancelCtx: cancel,
	}
}

func (s *streamCtl) cancel() {
	s.closedOnce.Do(func() {
		s.closed.Store(true)
		s.cancelCtx()
		if s.cmd != nil && s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		}
	})
}

// runDirect spawns `tail -n SCROLLBACK -F <path>` as the current uid.
func (s *streamCtl) runDirect(ctx context.Context) {
	argv := tailArgv(s.req.Path)
	log.Printf("wash-syslogs stream gen=%d path=%q as_root=false", s.gen, s.req.Path)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.sendState("failed", "stdout pipe: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.sendState("failed", "stderr pipe: "+err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		s.sendState("failed", "start: "+err.Error())
		return
	}
	s.cmd = cmd
	s.sendState("running", "")

	go s.flushLoop()

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			s.handleLine(sc.Text())
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				s.mu.Lock()
				if len(s.stderrAcc) < 8192 {
					s.stderrAcc = append(s.stderrAcc, buf[:n]...)
				}
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		err := cmd.Wait()
		s.flushNow()
		if s.closed.Load() {
			return
		}
		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = -1
			}
		}
		s.mu.Lock()
		stderrTxt := string(s.stderrAcc)
		s.mu.Unlock()
		if exitCode != 0 && looksLikePermDenied(stderrTxt) {
			s.sendState("perm_denied", strings.TrimSpace(stderrTxt))
			return
		}
		if exitCode == 0 {
			s.sendState("closed", "")
		} else {
			msg := strings.TrimSpace(stderrTxt)
			if msg == "" {
				msg = "tail exit " + strconv.Itoa(exitCode)
			}
			s.sendState("failed", msg)
		}
	}()
}

// runPriv routes the same tail invocation through wash-priv. Same
// dance as wash-journal — kept verbatim here for clarity rather than
// shared via an internal/sdk helper, since this is still only two
// consumers.
func (s *streamCtl) runPriv(_ context.Context) {
	argv := tailArgv(s.req.Path)
	reqID := newPrivStreamReqID()
	s.privReqID = reqID
	log.Printf("wash-syslogs stream gen=%d path=%q as_root=true req=%s", s.gen, s.req.Path, reqID)
	s.sendState("running", "(via wash-priv)")

	go s.flushLoop()

	err := s.conn.SendAppMsgTo(wire.Recipient{AppID: sdk.PrivAppID}, map[string]any{
		"kind":   "run_inline",
		"req_id": reqID,
		"argv":   argv,
		"reason": "tail " + s.req.Path,
	})
	if err != nil {
		s.sendState("failed", "wash-priv: "+err.Error())
	}
}

func (s *streamCtl) feedPriv(stream string, b []byte) {
	if s.closed.Load() {
		return
	}
	switch stream {
	case "stdout":
		s.privStdoutMu.Lock()
		s.privStdout = append(s.privStdout, b...)
		acc := s.privStdout
		for {
			idx := indexByte(acc, '\n')
			if idx < 0 {
				break
			}
			line := string(acc[:idx])
			acc = acc[idx+1:]
			s.handleLine(line)
		}
		s.privStdout = append(s.privStdout[:0], acc...)
		s.privStdoutMu.Unlock()
	case "stderr":
		s.privStderrMu.Lock()
		s.privStderr = append(s.privStderr, b...)
		s.privStderrMu.Unlock()
	}
}

func (s *streamCtl) finishPriv(exit int, errMsg string) {
	s.flushNow()
	if s.closed.Load() {
		return
	}
	if exit == 0 && errMsg == "" {
		s.sendState("closed", "")
		return
	}
	s.privStderrMu.Lock()
	stderrTxt := string(s.privStderr)
	s.privStderrMu.Unlock()
	if exit != 0 && looksLikePermDenied(stderrTxt) {
		s.sendState("perm_denied", strings.TrimSpace(stderrTxt))
		return
	}
	msg := strings.TrimSpace(stderrTxt)
	if msg == "" {
		msg = errMsg
	}
	if msg == "" {
		msg = "exit " + strconv.Itoa(exit)
	}
	s.sendState("failed", msg)
}

// handleLine parses one syslog text line into a LogEntry and queues
// it. Empty lines and `tail`'s "==> file <==" headers are skipped.
//
// Binary-content guard: if the line isn't valid UTF-8, scrub it
// before parsing. CBOR (the BE↔router transport) encodes Go strings
// as text-strings and the router's decode side strictly validates
// UTF-8 — a single bad byte tears down the app's read loop, leaving
// the user with a dead connection. Defensive sanitisation keeps a
// misclassified binary file (sysstat artefacts that slip past the
// discovery filter, etc.) from breaking the whole window.
func (s *streamCtl) handleLine(line string) {
	if line == "" {
		return
	}
	if strings.HasPrefix(line, "==> ") && strings.HasSuffix(line, " <==") {
		return
	}
	line = ensureUTF8(line)
	entry := parseSyslog(line)
	entry.Raw = ensureUTF8(entry.Raw)
	entry.Message = ensureUTF8(entry.Message)
	entry.Ident = ensureUTF8(entry.Ident)
	s.mu.Lock()
	s.pending = append(s.pending, entry)
	flush := len(s.pending) >= flushAt
	s.mu.Unlock()
	if flush {
		s.flushNow()
	}
}

// ensureUTF8 returns s unchanged when it's already valid UTF-8;
// otherwise it replaces invalid bytes with the Unicode replacement
// character. utf8.ValidString is O(n) but fast — cheap insurance
// against a binary file whose name happens to match looksLikeLogFile.
func ensureUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

func (s *streamCtl) flushLoop() {
	t := time.NewTicker(batchInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			s.flushNow()
			return
		case <-t.C:
			s.flushNow()
		}
	}
}

func (s *streamCtl) flushNow() {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.pending
	s.pending = nil
	s.mu.Unlock()
	if s.closed.Load() {
		return
	}
	if err := s.conn.SendAppMsg(map[string]any{
		"kind":  "lines",
		"gen":   s.gen,
		"lines": batch,
	}); err != nil {
		log.Printf("wash-syslogs flush: %v", err)
	}
}

func (s *streamCtl) sendState(status, errMsg string) {
	if s.closed.Load() && status != "closed" {
		return
	}
	_ = s.conn.SendAppMsg(map[string]any{
		"kind":    "stream_state",
		"gen":     s.gen,
		"status":  status,
		"error":   errMsg,
		"path":    s.req.Path,
		"as_root": s.req.AsRoot,
	})
}

// ----- helpers -----

// tailArgv constructs the tail invocation. `-F` follows by name,
// surviving rotations and truncations; `-n N` pre-loads scrollback.
func tailArgv(path string) []string {
	return []string{"tail", "-n", strconv.Itoa(scrollback), "-F", path}
}

func looksLikePermDenied(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "permission denied") ||
		strings.Contains(low, "operation not permitted") ||
		strings.Contains(low, "cannot open")
}

// bsdSyslogRE matches the classic RFC-3164 prefix used by every
// long-lived syslog daemon: "Mon DD HH:MM:SS host program[pid]: msg".
// Year is implicit (the daemon doesn't write it). PID may be absent.
var bsdSyslogRE = regexp.MustCompile(
	`^([A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2}) +([^ ]+) +([^\[\]: ]+)(?:\[(\d+)\])?: ?(.*)$`,
)

// iso8601RE matches the rsyslog `RSYSLOG_FileFormat` / `--output=iso`
// prefix that newer setups emit by default:
//   "2026-05-22T22:38:17.123456+00:00 host program[pid]: msg"
var iso8601RE = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:[Zz]|[+-]\d{2}:?\d{2})?) +([^ ]+) +([^\[\]: ]+)(?:\[(\d+)\])?: ?(.*)$`,
)

// parseSyslog tries the structured prefixes in order and falls back
// to "everything is the message" so we never drop a line. The point
// of best-effort parsing is to populate ts/ident/pid when we can —
// rendering the raw line is always the safety net.
func parseSyslog(line string) LogEntry {
	if m := iso8601RE.FindStringSubmatch(line); m != nil {
		return LogEntry{
			TS:       parseISO8601Micros(m[1]),
			Priority: guessPriority(m[5]),
			Ident:    m[3],
			PID:      atoi(m[4]),
			Message:  m[5],
			Raw:      line,
		}
	}
	if m := bsdSyslogRE.FindStringSubmatch(line); m != nil {
		return LogEntry{
			TS:       parseBSDMicros(m[1]),
			Priority: guessPriority(m[5]),
			Ident:    m[3],
			PID:      atoi(m[4]),
			Message:  m[5],
			Raw:      line,
		}
	}
	// Unstructured — pass through verbatim.
	return LogEntry{Priority: -1, Message: line, Raw: line}
}

// guessPriority inspects the message body for severity hints. Classic
// /var/log/messages has no <PRI> by the time it hits disk, so we
// look for words. Conservative: only return the strong signals; the
// default of -1 ("unknown") tells the FE to render neutrally.
func guessPriority(msg string) int {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "emerg") || strings.Contains(low, "fatal"):
		return 0
	case strings.Contains(low, "alert"):
		return 1
	case strings.Contains(low, "crit"):
		return 2
	case strings.Contains(low, "error") || strings.Contains(low, " err ") || strings.HasPrefix(low, "err:"):
		return 3
	case strings.Contains(low, "warn"):
		return 4
	case strings.Contains(low, "notice"):
		return 5
	case strings.Contains(low, "debug"):
		return 7
	}
	return -1
}

// parseBSDMicros parses "Jan  2 15:04:05" into the current year, in
// local time, as microseconds since epoch. The year-rollover edge
// case (December → January): we pick the year that keeps the result
// within ~6 months of now, in either direction. Imperfect but matches
// what humans expect when reading a journal across new year's eve.
func parseBSDMicros(s string) int64 {
	now := time.Now()
	// Time.Parse handles the BSD layout but needs a year prepended.
	t, err := time.ParseInLocation("2006 Jan _2 15:04:05", strconv.Itoa(now.Year())+" "+s, time.Local)
	if err != nil {
		return 0
	}
	// If the parse landed >6 months in the future, the line is from
	// last year (e.g. parsing "Dec 31" in early January).
	if t.After(now.Add(6 * 30 * 24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t.UnixMicro()
}

// parseISO8601Micros parses RFC3339 / ISO8601 prefixes. We tolerate
// the `Z` shorthand, `+HH:MM` offsets, and sub-second components.
func parseISO8601Micros(s string) int64 {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999Z07:00",
		"2006-01-02T15:04:05Z07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMicro()
		}
	}
	return 0
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func newPrivStreamReqID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "sl-" + hex.EncodeToString(b[:])
}
