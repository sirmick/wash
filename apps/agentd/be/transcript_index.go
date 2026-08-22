// Full-text index over stored transcripts (docs/AGENT_UX.md §History).
//
// The search it accelerates is the one the History panel already makes:
// "which stored conversations contain this?", typed a character at a time
// with a 150ms debounce. Without an index that is a read of every
// transcript on every keystroke — a few milliseconds today at ~1MB of
// history, and seconds after a year of it.
//
// TRIGRAMS, not words. The panel searches as you type, so "recon" must
// find "reconnect" before you finish typing it; a word index would answer
// nothing until the word was complete, which is a worse search that
// happens to be faster. A trigram index preserves the substring semantics
// exactly: a query's 3-grams narrow the field to candidate sessions, and
// the existing line-by-line scan then confirms them and lifts the
// snippet. False positives are fine and expected — the confirm pass is
// the authority, the index only decides who gets read.
//
// Measured. On a real store (10 sessions, 982KB) the index is 15,922
// trigrams and 12.7% of the corpus. On a synthetic 200-session corpus
// (~4.6MB), one rare-term query costs (transcript_index_bench_test.go):
//
//	listing only, no query   9.7ms   ← the floor: head+tail of every file
//	indexed search          10.3ms   ← so the search itself is ~0.6ms
//	unindexed search        68.3ms   ← the same search before this file
//
// The search component went from ~58ms to under a millisecond; what is
// left is the directory listing the panel needs regardless. At a 150ms
// debounce that is the difference between typing and waiting.
//
// Entirely DERIVED state. There is no migration, no schema, and no
// corruption story worth the name: if the file is missing, unreadable or
// from another version, it is rebuilt from the transcripts, which cost
// ~23ms per megabyte to walk. That is also why it is not in the
// transcripts' own directory listing path — it is a cache, and the
// .jsonl files remain the truth.
//
// Staleness is per session and by (size, mtime): a query reconciles first,
// which in practice means re-indexing the ONE file the live session is
// appending to. Cheaper than tracking dirtiness through the writer, and
// self-healing if a write is missed entirely.

package agentd

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// indexFileName is the on-disk cache, a sibling of the transcripts it
// indexes. Deliberately NOT a .jsonl, so listSessionMeta's directory walk
// skips it without needing to know it exists.
const indexFileName = "search.idx"

// indexVer is bumped when the on-disk shape changes. A mismatch rebuilds
// rather than migrating — the data is derived, so the cheap answer is the
// right one.
const indexVer = 1

// minTermLen is the shortest query that can use the index. Below three
// characters there is no trigram to look up, so those queries fall back
// to scanning every session — correct, just not accelerated.
const minTermLen = 3

// idxEntry is one indexed session: the postings key plus what makes the
// entry stale.
type idxEntry struct {
	SessionID string `json:"s"`
	Size      int64  `json:"z"`
	ModMS     int64  `json:"m"`
	// Grams is this session's distinct trigram set, sorted. Stored per
	// session rather than as one global postings map because that is what
	// makes an incremental update possible: re-indexing one file replaces
	// one entry, and the postings map is rebuilt in memory from the
	// entries. At this corpus size that inversion is microseconds.
	Grams []string `json:"g"`
}

type idxFile struct {
	Version int        `json:"v"`
	Entries []idxEntry `json:"e"`
}

// searchIndex is the in-memory form: the entries plus their inversion.
type searchIndex struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]*idxEntry // by session id
	posting map[string][]string  // trigram → session ids
	dirty   bool
}

var idx = &searchIndex{entries: map[string]*idxEntry{}, posting: map[string][]string{}}

// trigramsOf returns the distinct 3-grams of s, lowercased.
//
// Byte-wise on purpose: the confirm pass is a byte-wise
// strings.Contains, so the index has to agree with it about what a
// substring is. Working in runes here would make the two disagree on
// exactly the inputs where it matters (a query that lands mid-rune),
// and the index would then rule out a session the confirm pass would
// have matched — a false NEGATIVE, which is the one kind of error this
// design cannot absorb.
func trigramsOf(s string, out map[string]bool) {
	s = strings.ToLower(s)
	for i := 0; i+minTermLen <= len(s); i++ {
		out[s[i:i+minTermLen]] = true
	}
}

// indexPath is the cache file, or "" when there is no state dir.
func indexPath() string {
	dir := transcriptDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, indexFileName)
}

// scanTranscriptGrams reads one transcript and returns its distinct
// trigrams. Mirrors searchTranscript's idea of what text IS: decoded
// event text and tool titles, never the raw JSON line and never an
// image's base64 payload.
func scanTranscriptGrams(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxImageBytes+(1<<16))
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Kind == EventImage {
			continue
		}
		trigramsOf(e.Text, seen)
		trigramsOf(e.Title, seen)
	}
	if err := sc.Err(); err != nil {
		// A truncated final line is normal for an append-only file being
		// written right now. Index what we read; the next reconcile picks
		// up the rest, because the size will have changed.
		log.Printf("agentd: index scan %s: %v (indexed what was readable)", filepath.Base(path), err)
	}
	grams := make([]string, 0, len(seen))
	for g := range seen {
		grams = append(grams, g)
	}
	sort.Strings(grams)
	return grams, nil
}

// invert rebuilds the trigram → sessions map from the entries. Called
// under mu after any entry set change.
func (x *searchIndex) invert() {
	posting := make(map[string][]string, len(x.posting))
	for id, e := range x.entries {
		for _, g := range e.Grams {
			posting[g] = append(posting[g], id)
		}
	}
	for _, ids := range posting {
		sort.Strings(ids)
	}
	x.posting = posting
}

// load reads the cache file. A missing, unreadable or wrong-version file
// is not an error: it leaves an empty index, which reconcile then fills.
func (x *searchIndex) load() {
	x.entries = map[string]*idxEntry{}
	x.posting = map[string][]string{}
	x.loaded = true
	p := indexPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var f idxFile
	if json.Unmarshal(b, &f) != nil || f.Version != indexVer {
		log.Printf("agentd: search index unreadable or v%d — rebuilding", f.Version)
		return
	}
	for i := range f.Entries {
		e := f.Entries[i]
		x.entries[e.SessionID] = &e
	}
	x.invert()
}

// save writes the cache atomically. Best-effort by design: losing it
// costs a rebuild, so a failure is worth a log line and nothing more.
func (x *searchIndex) save() {
	if !x.dirty {
		return
	}
	p := indexPath()
	if p == "" {
		return
	}
	out := idxFile{Version: indexVer, Entries: make([]idxEntry, 0, len(x.entries))}
	ids := make([]string, 0, len(x.entries))
	for id := range x.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids) // stable file, so a diff of two runs is meaningful
	for _, id := range ids {
		out.Entries = append(out.Entries, *x.entries[id])
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("agentd: search index write: %v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("agentd: search index rename: %v", err)
		_ = os.Remove(tmp)
		return
	}
	x.dirty = false
}

// reconcile brings the index in line with the directory: new transcripts
// are indexed, changed ones re-indexed, deleted ones dropped.
//
// Cost is one Stat per session plus a full read of whatever actually
// changed — in a live session, exactly one file.
func (x *searchIndex) reconcile() {
	dir := transcriptDir()
	if dir == "" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	live := make(map[string]bool, len(ents))
	for _, de := range ents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(de.Name(), ".jsonl")
		live[id] = true
		modMS := fi.ModTime().UnixMilli()
		if cur, ok := x.entries[id]; ok && cur.Size == fi.Size() && cur.ModMS == modMS {
			continue
		}
		grams, err := scanTranscriptGrams(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		x.entries[id] = &idxEntry{SessionID: id, Size: fi.Size(), ModMS: modMS, Grams: grams}
		x.dirty = true
	}
	for id := range x.entries {
		if !live[id] {
			delete(x.entries, id)
			x.dirty = true
		}
	}
	if x.dirty {
		x.invert()
	}
}

// candidates returns the session ids that could contain every term, or
// ok=false when the index cannot answer (a term shorter than a trigram)
// and the caller must consider every session.
//
// The answer is a SUPERSET: trigrams prove which characters appear, not
// in what order, so "cab" and "abc" share no trigram but "abcab" and
// "cabab" do. Callers confirm.
func (x *searchIndex) candidates(terms []string) ([]string, bool) {
	var out []string
	first := true
	for _, t := range terms {
		if len(t) < minTermLen {
			return nil, false
		}
		grams := map[string]bool{}
		trigramsOf(t, grams)
		for g := range grams {
			ids := x.posting[g]
			if first {
				out = append([]string(nil), ids...)
				first = false
				continue
			}
			out = intersect(out, ids)
			if len(out) == 0 {
				return nil, true
			}
		}
	}
	if first {
		return nil, false // no usable terms
	}
	return out, true
}

// intersect returns the sorted intersection of two sorted id slices.
func intersect(a, b []string) []string {
	out := a[:0:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// searchCandidates is the package entry point: reconcile, then answer.
// ok=false means "index can't narrow this — read everything".
func searchCandidates(terms []string) (map[string]bool, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if !idx.loaded {
		idx.load()
	}
	start := time.Now()
	idx.reconcile()
	ids, ok := idx.candidates(terms)
	idx.save()
	if !ok {
		return nil, false
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		log.Printf("agentd: search index slow: %v for %d session(s)", took.Round(time.Millisecond), len(idx.entries))
	}
	return set, true
}

// indexStats is for tests and the About panel: how many sessions and
// trigrams the index holds.
func indexStats() (sessions, grams int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if !idx.loaded {
		idx.load()
	}
	idx.reconcile()
	return len(idx.entries), len(idx.posting)
}

// resetIndexForTest drops all state. Tests set XDG_STATE_HOME per case,
// and the index is a package-level singleton.
func resetIndexForTest() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries = map[string]*idxEntry{}
	idx.posting = map[string][]string{}
	idx.loaded = false
	idx.dirty = false
}
