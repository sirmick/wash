package bulk

import "testing"

func TestExternalStoreUpsertOrder(t *testing.T) {
	e := newExternalStore()
	e.upsert(JobView{JobID: "up-1", Op: "upload", Status: "running", Total: 100}, "inst-a")
	e.upsert(JobView{JobID: "up-2", Op: "upload", Status: "running", Total: 50}, "inst-b")

	// Progress update of up-1 must replace in place, not reorder.
	e.upsert(JobView{JobID: "up-1", Op: "upload", Status: "running", Done: 40, Total: 100}, "")

	views := e.views()
	if len(views) != 2 {
		t.Fatalf("views len = %d, want 2", len(views))
	}
	if views[0].JobID != "up-1" || views[1].JobID != "up-2" {
		t.Errorf("order = %v, want [up-1 up-2]", []string{views[0].JobID, views[1].JobID})
	}
	if views[0].Done != 40 {
		t.Errorf("up-1 Done = %d, want 40 (in-place update)", views[0].Done)
	}
}

func TestExternalStoreOwner(t *testing.T) {
	e := newExternalStore()
	e.upsert(JobView{JobID: "up-1", Status: "running"}, "inst-a")
	// A progress update with empty owner must not clobber the recorded one.
	e.upsert(JobView{JobID: "up-1", Status: "running", Done: 10}, "")

	if o, ok := e.ownerOf("up-1"); !ok || o != "inst-a" {
		t.Errorf("ownerOf(up-1) = %q,%v, want inst-a,true", o, ok)
	}
	if _, ok := e.ownerOf("nope"); ok {
		t.Errorf("ownerOf(nope) should be false")
	}
}
