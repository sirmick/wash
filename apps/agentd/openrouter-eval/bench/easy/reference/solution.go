package easy

// The reference solution, used only to check the hidden tests themselves
// (make bench-selfcheck); it is never shown to a model.

import (
	"sort"
	"strings"
)

func Clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}

func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range title {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
			fallthrough
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return b.String()
}

func TopWords(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	counts := map[string]int{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return r < 'a' || r > 'z' }) {
		counts[w]++
	}
	words := make([]string, 0, len(counts))
	for w := range counts {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool {
		if counts[words[i]] != counts[words[j]] {
			return counts[words[i]] > counts[words[j]]
		}
		return words[i] < words[j]
	})
	return words[:min(n, len(words))]
}
