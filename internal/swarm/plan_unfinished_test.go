package swarm

import (
	"reflect"
	"testing"
)

func TestUnfinished(t *testing.T) {
	w := &Workspace{
		Plan: []Node{
			{ID: "A", State: "active"},   // an open assignment: working
			{ID: "B", State: "active"},   // a live member on it: working
			{ID: "C", State: "active"},   // nobody: stale
			{ID: "D", State: "active"},   // only an ended member and a done assignment: stale
			{ID: "E", State: "reported"}, // awaiting acceptance
			{ID: "F", State: "done"},
			{ID: "G", State: "todo"},
		},
		Assignments: []Assignment{
			{ID: "a1", Node: "A", State: "active"},
			{ID: "a2", Node: "D", State: "done"},
		},
		Members: []Member{
			{ID: "m1", Node: "B", State: "available"},
			{ID: "m2", Node: "D", State: "ended"},
		},
	}
	working, reported, stale := Unfinished(w)
	if !reflect.DeepEqual(working, []string{"A", "B"}) {
		t.Errorf("working = %v", working)
	}
	if !reflect.DeepEqual(reported, []string{"E"}) {
		t.Errorf("reported = %v", reported)
	}
	if !reflect.DeepEqual(stale, []string{"C", "D"}) {
		t.Errorf("stale = %v", stale)
	}
}
