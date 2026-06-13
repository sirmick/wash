package journal

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// scrollback is how many entries journalctl pre-loads before tailing.
// Big enough that opening a unit shows enough context to be useful;
// small enough that the first paint isn't a slog.
const scrollback = 500

// batchInterval bounds how often we flush a batch of parsed entries
// to the FE. 100ms keeps the UI responsive without flooding the
// router on a chatty unit.
const batchInterval = 100 * time.Millisecond

// LogEntry is the wire-shape one journal record. Fields lowercased
// for the FE; we drop everything we don't render.
type LogEntry struct {
	TS       int64  `json:"ts"`               // microseconds since epoch
	Priority int    `json:"priority"`   // 0..7 (0 = emerg)
	Unit     string `json:"unit"`           // _SYSTEMD_UNIT
	Ident    string `json:"ident"`         // SYSLOG_IDENTIFIER or _COMM
	PID      int    `json:"pid"`             // _PID
	Message  string `json:"message"`     // MESSAGE
}

// streamCtl owns one running journalctl (either direct subprocess or
// driven via wash-priv). Its lifetime is one selection: a new select
// from the FE cancels the previous streamCtl and creates a new one.
type streamCtl struct {
	conn *sdk.Conn
	gen  int64
	req  selectReq

	ctx       context.Context
	cancelCtx context.CancelFunc

	// Direct-mode plumbing.
	cmd *exec.Cmd

	// Priv-mode plumbing. We assemble stdout/stderr bytes from
	// fragmented priv.stream callbacks before line-splitting.
	privReqID    string
	privStdout   []byte
	privStderr   []byte
	privStdoutMu sync.Mutex
	privStderrMu sync.Mutex

	// Batched send pipeline. Writer goroutines push entries into
	// pending; a flusher drains every batchInterval (or earlier if
	// pending grows past flushAt).
	mu         sync.Mutex
	pending    []LogEntry
	stderrAcc  []byte // unprivileged stderr — inspected for perm denied
	closedOnce sync.Once
	closed     atomic.Bool

	// parseDrops counts JSON-looking lines that failed to decode.
	// Logged on the first drop only — journalctl interleaving non-JSON
	// warnings is known-normal, a *stream* of failures is not.
	parseDrops atomic.Int64
}

const flushAt = 200

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
			// SIGTERM the whole process group — journalctl with -f
			// otherwise sits waiting for new events and only the
			// parent dies.
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		}
	})
}

// runDirect spawns journalctl as the current user.
func (s *streamCtl) runDirect(ctx context.Context) {
	argv := journalArgv(s.req)
	log.Printf("wash-journal stream gen=%d unit=%q range=%s priority=%d as_root=false",
		s.gen, s.req.Unit, s.req.Range, priorityOf(s.req))

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Own process group so cancel() can kill journalctl + any
	// helpers in one shot.
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

	// stdout: line-split JSON, parse, batch.
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			s.handleLine(sc.Bytes())
		}
	}()
	// stderr: accumulate so we can detect permission-denied when
	// journalctl exits non-zero.
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
			// Cancelled — don't emit a state change; a new stream is
			// being spun up by the caller.
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
				msg = "journalctl exit " + strconv.Itoa(exitCode)
			}
			s.sendState("failed", msg)
		}
	}()
}

// runPriv routes the same journalctl invocation through wash-priv as
// run_inline. wash-priv handles approval + sudo password; bytes come
// back as priv.stream messages dispatched by onAppMsgFrom.
func (s *streamCtl) runPriv(_ context.Context) {
	argv := journalArgv(s.req)
	reqID := newPrivStreamReqID()
	s.privReqID = reqID
	log.Printf("wash-journal stream gen=%d unit=%q range=%s priority=%d as_root=true req=%s",
		s.gen, s.req.Unit, s.req.Range, priorityOf(s.req), reqID)
	s.sendState("running", "(via wash-priv)")

	go s.flushLoop()

	// Fire the request. wash-priv replies async on this app's event
	// channel; onAppMsgFrom dispatches priv.stream / priv.result.
	err := s.conn.SendAppMsgTo(wire.Recipient{AppID: sdk.PrivAppID}, map[string]any{
		"kind":   "run_inline",
		"req_id": reqID,
		"argv":   argv,
		"reason": "tail journalctl",
	})
	if err != nil {
		s.sendState("failed", "wash-priv: "+err.Error())
	}
}

// feedPriv pushes a chunk from wash-priv's stream into the per-stream
// accumulator and emits whole lines via handleLine.
func (s *streamCtl) feedPriv(stream string, b []byte) {
	if s.closed.Load() {
		return
	}
	switch stream {
	case "stdout":
		s.privStdoutMu.Lock()
		s.privStdout = append(s.privStdout, b...)
		// Split on newlines; keep trailing partial.
		acc := s.privStdout
		for {
			idx := indexByte(acc, '\n')
			if idx < 0 {
				break
			}
			line := acc[:idx]
			acc = acc[idx+1:]
			// Copy because handleLine mutates the JSON decoder state
			// asynchronously of our locked buffer.
			cp := make([]byte, len(line))
			copy(cp, line)
			s.handleLine(cp)
		}
		s.privStdout = append(s.privStdout[:0], acc...)
		s.privStdoutMu.Unlock()
	case "stderr":
		s.privStderrMu.Lock()
		s.privStderr = append(s.privStderr, b...)
		s.privStderrMu.Unlock()
	}
}

// finishPriv handles priv.result for this stream.
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
		// Already running as root — perm denied here is genuine.
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

// handleLine parses one JSON-formatted journal record and queues a
// LogEntry for the FE. Empty / unparseable lines are dropped silently
// — journalctl occasionally emits warnings to stdout that aren't JSON.
func (s *streamCtl) handleLine(line []byte) {
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		if s.parseDrops.Add(1) == 1 {
			log.Printf("wash-journal: dropping unparseable journalctl line (logged once per stream): %v", err)
		}
		return
	}
	entry := LogEntry{
		TS:       parseJournalInt(raw["__REALTIME_TIMESTAMP"]),
		Priority: int(parseJournalInt(raw["PRIORITY"])),
		Unit:     toStr(raw["_SYSTEMD_UNIT"]),
		Ident:    firstStr(raw["SYSLOG_IDENTIFIER"], raw["_COMM"]),
		PID:      int(parseJournalInt(raw["_PID"])),
		Message:  toStr(raw["MESSAGE"]),
	}
	// MESSAGE can be an array of byte ints when journald couldn't
	// decode it as UTF-8. Render those as a hex placeholder rather
	// than crashing the parser.
	if entry.Message == "" {
		if arr, ok := raw["MESSAGE"].([]any); ok && len(arr) > 0 {
			entry.Message = "(binary " + strconv.Itoa(len(arr)) + "B)"
		}
	}
	s.mu.Lock()
	s.pending = append(s.pending, entry)
	flush := len(s.pending) >= flushAt
	s.mu.Unlock()
	if flush {
		s.flushNow()
	}
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
		log.Printf("wash-journal flush: %v", err)
	}
}

func (s *streamCtl) sendState(status, errMsg string) {
	if s.closed.Load() && status != "closed" {
		return
	}
	_ = s.conn.SendAppMsg(map[string]any{
		"kind":   "stream_state",
		"gen":    s.gen,
		"status": status,
		"error":  errMsg,
		"unit":   s.req.Unit,
		"as_root": s.req.AsRoot,
	})
}

// ----- helpers -----

func journalArgv(req selectReq) []string {
	argv := []string{"journalctl", "-o", "json", "--no-pager", "-n", strconv.Itoa(scrollback), "-f"}
	switch req.Range {
	case "boot", "":
		argv = append(argv, "-b")
	case "hour":
		argv = append(argv, "--since", "1 hour ago")
	case "day":
		argv = append(argv, "--since", "1 day ago")
	case "all":
		// no range filter
	}
	if req.Unit != "" {
		argv = append(argv, "-u", req.Unit)
	}
	p := priorityOf(req)
	if p > 0 && p <= 7 {
		argv = append(argv, "-p", strconv.Itoa(p))
	}
	return argv
}

// priorityOf coerces selectReq.Priority through sdk.ToInt64 so the
// JS-Number → JSON float64 path decodes cleanly. The field is typed
// `any` on the struct; pre-typed int values are also handled.
func priorityOf(req selectReq) int {
	return int(sdk.ToInt64(req.Priority))
}

func looksLikePermDenied(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "permission denied") ||
		strings.Contains(low, "operation not permitted") ||
		strings.Contains(low, "no journal files were opened") ||
		strings.Contains(low, "not in the 'systemd-journal' group")
}

// parseJournalInt unpacks journalctl's JSON numeric fields. journalctl
// emits everything as strings ("12345") for safety against JS-side
// precision loss; we still tolerate raw numbers.
func parseJournalInt(v any) int64 {
	switch x := v.(type) {
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func toStr(v any) string {
	s, _ := v.(string)
	return s
}

func firstStr(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
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
	return "j-" + hex.EncodeToString(b[:])
}
