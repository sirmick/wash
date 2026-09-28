package easy

import (
	"slices"
	"testing"
)

func TestTopWords(t *testing.T) {
	got := TopWords("the cat and the hat", 2)
	if want := []string{"the", "and"}; !slices.Equal(got, want) {
		t.Errorf("TopWords = %q, want %q", got, want)
	}
}
