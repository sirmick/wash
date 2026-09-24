// Recursive name search for the FE's "search subtree" mode (the filter
// box's Ctrl+Shift+F toggle). A bounded, cancellable walk from one
// folder, matching entry NAMES by case-insensitive substring — not a
// content grep, and not an index: fm answers "where is that file under
// here" and stops well before it could hurt (depth, visited and hit caps).
//
// Wire shape:
//
//	FE → fm   search        {search_id, dir, query}   → search_ok {search_id}
//	fm → FE   search_batch  {search_id, hits:[…]}      (pushed, ≤ searchBatch per push)
//	fm → FE   search_done   {search_id, hits, visited, truncated, cancelled}
//	FE → fm   search_cancel {search_id}                (fire-and-forget)
//
// The walk runs in its own goroutine so the SDK read loop stays free to
// see the cancel — a handler that walked inline could never be stopped.
// Symlinked directories are not followed (loops), unreadable directories
// are skipped and counted, and everything stays inside the sandbox root
// because the start dir is confined and the walk only descends.
package fm

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/pkg/sdk"
)

const (
	searchMaxDepth   = 16
	searchMaxVisited = 200_000
	searchMaxHits    = 2_000
	searchBatch      = 200
)

type searchReq struct {
	SearchID string `json:"search_id"`
	Dir      string `json:"dir"`
	Query    string `json:"query"`
}

type searchResp struct {
	SearchID string `json:"search_id"`
	Dir      string `json:"dir"`
}

type searchCancelReq struct {
	SearchID string `json:"search_id"`
}

// searchHit is one matching entry: its absolute path, its path relative to
// the search root (what the result row shows), and the usual entry
// metadata so the row renders like any other.
type searchHit struct {
	Path  string    `json:"path"`
	Rel   string    `json:"rel"`
	Entry wfs.Entry `json:"entry"`
}

type searchOpts struct {
	MaxDepth   int
	MaxVisited int
	MaxHits    int
}

type searchStats struct {
	Hits      int
	Visited   int
	Skipped   int // unreadable directories
	Truncated bool
	Cancelled bool
}

var (
	searchesMu sync.Mutex
	searches   = map[string]*searchRun{}
)

type searchRun struct {
	cancelled bool
	mu        sync.Mutex
}

func (r *searchRun) cancel() {
	r.mu.Lock()
	r.cancelled = true
	r.mu.Unlock()
}

func (r *searchRun) isCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled
}

func registerSearchHandlers(b *sdk.Bus) {
	sdk.Handle(b, "search", func(_ *sdk.Conn, _ string, req searchReq) (searchResp, error) {
		if req.SearchID == "" || strings.TrimSpace(req.Query) == "" {
			return searchResp{}, sdk.Errf(sdk.ErrBadRequest, "missing search_id or query")
		}
		abs, err := fmFS.Confine(req.Dir)
		if err != nil {
			log.Printf("fm: search dir=%q q=%q: %v", req.Dir, req.Query, err)
			return searchResp{}, fsErr(err, req.Dir)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return searchResp{}, sdk.Errf("not_dir", "search root is not a directory")
		}
		run := &searchRun{}
		searchesMu.Lock()
		searches[req.SearchID] = run
		searchesMu.Unlock()
		go runSearch(req.SearchID, abs, req.Query, run)
		return searchResp{SearchID: req.SearchID, Dir: abs}, nil
	})
	sdk.HandleVoid(b, "search_cancel", func(_ *sdk.Conn, _ string, req searchCancelReq) error {
		searchesMu.Lock()
		run := searches[req.SearchID]
		searchesMu.Unlock()
		if run != nil {
			run.cancel()
		}
		return nil
	})
}

func runSearch(id, dir, query string, run *searchRun) {
	defer func() {
		searchesMu.Lock()
		delete(searches, id)
		searchesMu.Unlock()
	}()
	batch := make([]searchHit, 0, searchBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = bus.Emit("search_batch", map[string]any{"search_id": id, "hits": batch})
		batch = make([]searchHit, 0, searchBatch)
	}
	stats := searchWalk(fmFS, dir, query, searchOpts{
		MaxDepth: searchMaxDepth, MaxVisited: searchMaxVisited, MaxHits: searchMaxHits,
	}, run.isCancelled, func(h searchHit) {
		batch = append(batch, h)
		if len(batch) >= searchBatch {
			flush()
		}
	})
	flush()
	log.Printf("fm: search dir=%q q=%q hits=%d visited=%d skipped=%d truncated=%v cancelled=%v",
		dir, query, stats.Hits, stats.Visited, stats.Skipped, stats.Truncated, stats.Cancelled)
	_ = bus.Emit("search_done", map[string]any{
		"search_id": id, "hits": stats.Hits, "visited": stats.Visited,
		"skipped": stats.Skipped, "truncated": stats.Truncated, "cancelled": stats.Cancelled,
	})
}

// searchWalk is the pure walker: depth-first from root, names compared
// case-insensitively against query, hits reported through emit in
// directory order (dirs sorted by name within each folder). Stops early —
// Truncated — when MaxVisited entries have been examined or MaxHits hits
// reported; Cancelled when cancelled() turns true between entries.
// Unreadable directories are skipped (Skipped counts them); symlinks are
// matched as entries but never descended.
func searchWalk(fsys *wfs.FS, root, query string, o searchOpts, cancelled func() bool, emit func(searchHit)) searchStats {
	q := strings.ToLower(strings.TrimSpace(query))
	var st searchStats
	if q == "" {
		return st
	}
	type frame struct {
		dir   string
		depth int
	}
	stack := []frame{{root, 0}}
	for len(stack) > 0 {
		if cancelled != nil && cancelled() {
			st.Cancelled = true
			return st
		}
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(f.dir)
		if err != nil {
			st.Skipped++
			continue
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		// Push subdirs in reverse so the stack pops them in name order.
		var subdirs []string
		for _, e := range entries {
			if st.Visited >= o.MaxVisited {
				st.Truncated = true
				return st
			}
			st.Visited++
			p := filepath.Join(f.dir, e.Name())
			if strings.Contains(strings.ToLower(e.Name()), q) {
				if st.Hits >= o.MaxHits {
					st.Truncated = true
					return st
				}
				entry, _, serr := fsys.Stat(p)
				if serr == nil {
					rel, _ := filepath.Rel(root, p)
					emit(searchHit{Path: p, Rel: filepath.ToSlash(rel), Entry: entry})
					st.Hits++
				}
			}
			if e.IsDir() && f.depth+1 < o.MaxDepth {
				subdirs = append(subdirs, p)
			}
		}
		for i := len(subdirs) - 1; i >= 0; i-- {
			stack = append(stack, frame{subdirs[i], f.depth + 1})
		}
	}
	return st
}
