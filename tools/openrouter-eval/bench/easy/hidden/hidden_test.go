package easy

// The held-back tests: copied into the finished project and run by the
// scorer, one subtest per scored case. The model never sees this file.

import (
	"fmt"
	"slices"
	"testing"
)

func TestHiddenClamp(t *testing.T) {
	for _, c := range []struct{ v, lo, hi, want int }{
		{-100, -5, 5, -5}, {-5, -5, 5, -5}, {5, -5, 5, 5}, {6, -5, 5, 5}, {3, 3, 3, 3}, {0, 1, 2, 1},
	} {
		t.Run(fmt.Sprint(c.v, c.lo, c.hi), func(t *testing.T) {
			if got := Clamp(c.v, c.lo, c.hi); got != c.want {
				t.Errorf("Clamp(%d, %d, %d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
			}
		})
	}
}

func TestHiddenSlug(t *testing.T) {
	for _, c := range [][2]string{
		{"Hello, World!", "hello-world"},
		{"  Go 1.22 notes", "go-1-22-notes"},
		{"***", ""},
		{"", ""},
		{"already-a-slug", "already-a-slug"},
		{"MiXeD CaSe", "mixed-case"},
		{"a--b__c", "a-b-c"},
		{"Café crème", "caf-cr-me"},
		{"trailing!!!", "trailing"},
		{"---lead", "lead"},
		{"tab\tand\nnewline", "tab-and-newline"},
		{"Ümlaut über alles", "mlaut-ber-alles"},
		{"x", "x"},
		{"2026 roadmap: Q3/4", "2026-roadmap-q3-4"},
	} {
		t.Run(c[0], func(t *testing.T) {
			if got := Slug(c[0]); got != c[1] {
				t.Errorf("Slug(%q) = %q, want %q", c[0], got, c[1])
			}
		})
	}
}

func TestHiddenTopWords(t *testing.T) {
	for _, c := range []struct {
		text string
		n    int
		want []string
	}{
		{"the cat and the hat", 2, []string{"the", "and"}},
		{"b a c b a b", 3, []string{"b", "a", "c"}},
		{"Go go GO! gopher", 1, []string{"go"}},
		{"one two", 5, []string{"one", "two"}},
		{"anything", 0, nil},
		{"anything", -1, nil},
		{"", 3, nil},
		{"it's x2 well-known", 10, []string{"it", "known", "s", "well", "x"}},
		{"zeta alpha zeta beta alpha gamma", 2, []string{"alpha", "zeta"}},
	} {
		t.Run(fmt.Sprint(c.text, c.n), func(t *testing.T) {
			got := TopWords(c.text, c.n)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("TopWords(%q, %d) = %q, want %q", c.text, c.n, got, c.want)
			}
		})
	}
}
