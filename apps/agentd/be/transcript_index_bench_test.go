package agentd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeCorpus lays down n synthetic transcripts of roughly `lines` events
// each, with one rare term buried in exactly one of them.
func writeCorpus(t testing.TB, dir string, n, lines int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		var b []byte
		b = append(b, []byte(fmt.Sprintf(`{"kind":"meta","v":1,"session_id":"s-%d","started_ms":1}`+"\n", i))...)
		for j := 0; j < lines; j++ {
			text := fmt.Sprintf("the scheduler dropped a frame while reconnecting the shell session %d line %d", i, j)
			if i == n/2 && j == lines/2 {
				text = "the quokka protocol is the thing nobody else mentions"
			}
			b = append(b, []byte(fmt.Sprintf(`{"seq":%d,"kind":"message","text":%q,"at_ms":1}`+"\n", j, text))...)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s-%d.jsonl", i)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// BenchmarkHistoryQueryRareTerm is the case the index exists for: a term
// in one session out of many, typed a character at a time. Without an
// index every keystroke reads the whole corpus.
func BenchmarkHistoryQueryRareTerm(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("XDG_STATE_HOME", dir)
	resetIndexForTest()
	writeCorpus(b, filepath.Join(dir, "wash", "agent-transcripts"), 200, 300)

	// Warm: the first query builds the index, which is the one-off cost.
	if got := historyQuery("quokka", 0); len(got) != 1 {
		b.Fatalf("fixture wrong: %d hits", len(got))
	}
	var total int64
	if fis, _ := os.ReadDir(filepath.Join(dir, "wash", "agent-transcripts")); fis != nil {
		for _, fi := range fis {
			if info, err := fi.Info(); err == nil {
				total += info.Size()
			}
		}
	}
	b.ReportMetric(float64(total)/(1<<20), "corpusMB")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := historyQuery("quokka", 0); len(got) != 1 {
			b.Fatalf("hits = %d", len(got))
		}
	}
}

// BenchmarkHistoryQueryRareTermNoIndex is the SAME query with the index
// forced off — the behaviour before it existed, kept as the comparison
// that justifies the file.
func BenchmarkHistoryQueryRareTermNoIndex(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("XDG_STATE_HOME", dir)
	resetIndexForTest()
	writeCorpus(b, filepath.Join(dir, "wash", "agent-transcripts"), 200, 300)

	scanAll := func(terms []string) int {
		n := 0
		for _, m := range listSessionMeta() {
			if matchesMeta(m, terms) {
				n++
				continue
			}
			if _, ok := searchTranscript(m.SessionID, terms); ok {
				n++
			}
		}
		return n
	}
	if got := scanAll([]string{"quokka"}); got != 1 {
		b.Fatalf("fixture wrong: %d hits", got)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := scanAll([]string{"quokka"}); got != 1 {
			b.Fatalf("hits = %d", got)
		}
	}
}

// BenchmarkHistoryListOnly is the floor: no query at all, so it is pure
// listSessionMeta (head + tail of every transcript). Whatever the indexed
// search costs above this is what searching actually costs.
func BenchmarkHistoryListOnly(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("XDG_STATE_HOME", dir)
	resetIndexForTest()
	writeCorpus(b, filepath.Join(dir, "wash", "agent-transcripts"), 200, 300)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := historyQuery("", 0); len(got) != 200 {
			b.Fatalf("listed %d", len(got))
		}
	}
}
