// Duplicate — "give me another one of these, right here" (Ctrl+D).
//
//	FE → fm   duplicate {paths}  → duplicate_ok {names, dest}
//
// The naming rule ("x (copy).txt", then "x (copy 2).txt") is
// bulkops.UniqueCopyName, shared with the "Keep both" answer to a copy
// conflict so a duplicate and a kept-both collision produce the same
// names. fm resolves the free names here — it is the side that can read
// the directory — and then hands the actual copying to wash-bulk as a
// single named job (bulkops.Job.Names), so a duplicated folder gets the
// queue's progress and cancel for free rather than blocking fm's BE.
//
// Every source must live in the same directory: Duplicate is a
// same-folder verb, and one job carries one Dest.
package fm

import (
	"log"
	"os"
	"path/filepath"

	"github.com/sirmick/wash/internal/bulkops"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

type duplicateReq struct {
	Paths []string `json:"paths"`
}

type duplicateResp struct {
	Dest  string   `json:"dest"`
	Names []string `json:"names"`
}

func registerDuplicateHandlers(b *sdk.Bus) {
	sdk.Handle(b, "duplicate", func(c *sdk.Conn, _ string, req duplicateReq) (duplicateResp, error) {
		if len(req.Paths) == 0 {
			return duplicateResp{}, sdk.Errf(sdk.ErrBadRequest, "nothing to duplicate")
		}
		abs := make([]string, 0, len(req.Paths))
		dest := ""
		for _, p := range req.Paths {
			a, err := fmFS.Confine(p)
			if err != nil {
				log.Printf("fm: duplicate path=%q: %v", p, err)
				return duplicateResp{}, fsErr(err, p)
			}
			dir := filepath.Dir(a)
			if dest == "" {
				dest = dir
			} else if dir != dest {
				return duplicateResp{}, sdk.Errf(sdk.ErrBadRequest, "duplicate: every item must be in the same folder")
			}
			abs = append(abs, a)
		}
		// One readdir for the whole batch, grown as names are claimed, so
		// duplicating three files at once can't hand out the same name
		// twice.
		taken, err := dirNames(dest)
		if err != nil {
			return duplicateResp{}, fsErr(err, dest)
		}
		names := make([]string, 0, len(abs))
		for _, a := range abs {
			st, serr := os.Lstat(a)
			if serr != nil {
				return duplicateResp{}, fsErr(serr, a)
			}
			name := bulkops.UniqueCopyName(filepath.Base(a), st.IsDir(), func(n string) bool { return taken[n] })
			taken[name] = true
			names = append(names, name)
		}
		log.Printf("fm: duplicate n=%d dest=%q names=%v", len(abs), dest, names)
		if err := c.SendAppMsgTo(wire.Recipient{AppID: bulkAppID}, map[string]any{
			"kind":  "enqueue",
			"op":    "copy",
			"paths": abs,
			"dest":  dest,
			"names": names,
		}); err != nil {
			return duplicateResp{}, err
		}
		return duplicateResp{Dest: dest, Names: names}, nil
	})
}

// dirNames is the set of entry names in dir — what UniqueCopyName probes
// against. A single readdir beats one Lstat per candidate when several
// items are duplicated at once.
func dirNames(dir string) (map[string]bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ents))
	for _, e := range ents {
		out[e.Name()] = true
	}
	return out, nil
}
