package team

// The held-back tests: copied into the finished project and run by the
// scorer, one subtest per scored case. The team never sees this file.

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

type sections = map[string]map[string]string

func TestHiddenINI(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     sections
	}{
		{"keys before a section", "a = 1\n", sections{"": {"a": "1"}}},
		{"section names keep case and are trimmed", "[ Server ]\nx=1\n", sections{"Server": {"x": "1"}}},
		{"keys lower-cased and trimmed", "[s]\n  MixedKey   =  v  \n", sections{"s": {"mixedkey": "v"}}},
		{"later key replaces", "[s]\na=1\nA=2\n", sections{"s": {"a": "2"}}},
		{"quoted value keeps spaces", "[s]\nv = \"  two  spaces \"\n", sections{"s": {"v": "  two  spaces "}}},
		{"escapes in quotes", "[s]\nv = \"say \\\"hi\\\" \\\\ bye\"\n", sections{"s": {"v": "say \"hi\" \\ bye"}}},
		{"comment lines", "; one\n# two\n   ; three\n[s]\na=1\n", sections{"s": {"a": "1"}}},
		{"comment characters after a value stay", "[s]\nurl = http://x/#top ; not a comment\n", sections{"s": {"url": "http://x/#top ; not a comment"}}},
		{"empty section still appears", "[empty]\n[s]\na=1\n", sections{"empty": {}, "s": {"a": "1"}}},
		{"blank lines and CRLF-free", "\n\n[s]\n\na=1\n\n", sections{"s": {"a": "1"}}},
		{"equals inside the value", "[s]\nexpr = a=b\n", sections{"s": {"expr": "a=b"}}},
		{"sections merge when repeated", "[s]\na=1\n[t]\nb=2\n[s]\nc=3\n", sections{"s": {"a": "1", "c": "3"}, "t": {"b": "2"}}},
		{"empty value", "[s]\na=\n", sections{"s": {"a": ""}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseINI(strings.NewReader(c.in))
			if err != nil {
				t.Fatalf("error %v", err)
			}
			if !maps.EqualFunc(got, c.want, func(a, b map[string]string) bool { return maps.Equal(a, b) }) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	for _, c := range []struct{ name, in, prefix string }{
		{"a line with no = is an error on its line", "[s]\na=1\nnonsense\n", "line 3:"},
		{"an unclosed section is an error", "a=1\n[broken\n", "line 2:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseINI(strings.NewReader(c.in))
			if err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
				t.Errorf("error %v, want it to start %q", err, c.prefix)
			}
		})
	}
}

func TestHiddenLRU(t *testing.T) {
	t.Run("evicts the least recently used", func(t *testing.T) {
		c := NewLRU(2)
		c.Put("a", "1")
		c.Put("b", "2")
		c.Put("c", "3")
		if _, ok := c.Get("a"); ok || c.Len() != 2 {
			t.Errorf("a kept or len %d", c.Len())
		}
	})
	t.Run("Get refreshes", func(t *testing.T) {
		c := NewLRU(2)
		c.Put("a", "1")
		c.Put("b", "2")
		c.Get("a")
		c.Put("c", "3")
		if _, ok := c.Get("b"); ok {
			t.Error("b should have gone, not a")
		}
		if v, ok := c.Get("a"); !ok || v != "1" {
			t.Errorf("a = %q %v", v, ok)
		}
	})
	t.Run("Put replaces and refreshes", func(t *testing.T) {
		c := NewLRU(2)
		c.Put("a", "1")
		c.Put("b", "2")
		c.Put("a", "9")
		c.Put("c", "3")
		if v, ok := c.Get("a"); !ok || v != "9" || c.Len() != 2 {
			t.Errorf("a = %q %v, len %d", v, ok, c.Len())
		}
	})
	t.Run("Keys from most to least recent", func(t *testing.T) {
		c := NewLRU(3)
		c.Put("a", "1")
		c.Put("b", "2")
		c.Put("c", "3")
		c.Get("a")
		if got := c.Keys(); !slices.Equal(got, []string{"a", "c", "b"}) {
			t.Errorf("Keys = %v", got)
		}
	})
	t.Run("a missing key", func(t *testing.T) {
		if v, ok := NewLRU(1).Get("x"); ok || v != "" {
			t.Errorf("got %q %v", v, ok)
		}
	})
	t.Run("capacity zero holds nothing", func(t *testing.T) {
		c := NewLRU(0)
		c.Put("a", "1")
		if _, ok := c.Get("a"); ok || c.Len() != 0 || len(c.Keys()) != 0 {
			t.Error("a zero cache held something")
		}
	})
	t.Run("capacity one", func(t *testing.T) {
		c := NewLRU(1)
		c.Put("a", "1")
		c.Put("b", "2")
		if got := c.Keys(); !slices.Equal(got, []string{"b"}) {
			t.Errorf("Keys = %v", got)
		}
	})
	t.Run("many entries", func(t *testing.T) {
		c := NewLRU(100)
		for i := 0; i < 1000; i++ {
			c.Put(fmt.Sprint(i), fmt.Sprint(i))
		}
		if v, ok := c.Get("950"); c.Len() != 100 || !ok || v != "950" {
			t.Errorf("len %d, 950 = %q %v", c.Len(), v, ok)
		}
		if _, ok := c.Get("899"); ok {
			t.Error("899 should have gone")
		}
	})
}

func TestHiddenWrap(t *testing.T) {
	for _, c := range []struct {
		name, text string
		width      int
		want       []string
	}{
		{"basic", "the quick brown fox", 10, []string{"the quick", "brown fox"}},
		{"exact width", "abc def", 7, []string{"abc def"}},
		{"one over", "abc def", 6, []string{"abc", "def"}},
		{"runs of spaces, tabs and newlines", "a  b\t\tc\n\nd", 80, []string{"a b c d"}},
		{"a long word is split", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"a long word after a short one", "hi abcdefgh", 4, []string{"hi", "abcd", "efgh"}},
		{"runes, not bytes", "héllo wörld", 5, []string{"héllo", "wörld"}},
		{"empty", "", 10, nil},
		{"only spaces", "   \t ", 10, nil},
		{"width zero", "abc", 0, nil},
		{"width one", "ab c", 1, []string{"a", "b", "c"}},
		{"leading and trailing space", "  a b  ", 3, []string{"a b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Wrap(c.text, c.width)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Wrap(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
			}
		})
	}
}
