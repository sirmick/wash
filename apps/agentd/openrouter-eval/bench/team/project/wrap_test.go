package team

import (
	"slices"
	"testing"
)

func TestWrap(t *testing.T) {
	if got, want := Wrap("the quick brown fox", 10), []string{"the quick", "brown fox"}; !slices.Equal(got, want) {
		t.Errorf("Wrap = %q, want %q", got, want)
	}
}
