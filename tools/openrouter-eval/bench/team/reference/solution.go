package team

// The reference solution, used only to check the hidden tests themselves
// (make bench-selfcheck); it is never shown to a model.

import (
	"bufio"
	"container/list"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

func ParseINI(r io.Reader) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	section := ""
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
			continue
		case line[0] == '[':
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: unclosed section", n)
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: no =", n)
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			return nil, fmt.Errorf("line %d: empty key", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			var b strings.Builder
			in := v[1 : len(v)-1]
			for i := 0; i < len(in); i++ {
				if in[i] == '\\' && i+1 < len(in) && (in[i+1] == '"' || in[i+1] == '\\') {
					i++
				}
				b.WriteByte(in[i])
			}
			v = b.String()
		}
		if out[section] == nil {
			out[section] = map[string]string{}
		}
		out[section][k] = v
	}
	return out, sc.Err()
}

type entry struct{ key, value string }

type LRU struct {
	Cap   int
	order *list.List
	index map[string]*list.Element
}

func NewLRU(capacity int) *LRU {
	return &LRU{Cap: capacity, order: list.New(), index: map[string]*list.Element{}}
}

func (c *LRU) Get(key string) (string, bool) {
	e, ok := c.index[key]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(e)
	return e.Value.(*entry).value, true
}

func (c *LRU) Put(key, value string) {
	if c.Cap <= 0 {
		return
	}
	if e, ok := c.index[key]; ok {
		e.Value.(*entry).value = value
		c.order.MoveToFront(e)
		return
	}
	c.index[key] = c.order.PushFront(&entry{key, value})
	if c.order.Len() > c.Cap {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.index, last.Value.(*entry).key)
	}
}

func (c *LRU) Len() int { return c.order.Len() }

func (c *LRU) Keys() []string {
	var keys []string
	for e := c.order.Front(); e != nil; e = e.Next() {
		keys = append(keys, e.Value.(*entry).key)
	}
	return keys
}

func Wrap(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(text) {
		for utf8.RuneCountInString(w) > width {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			r := []rune(w)
			lines = append(lines, string(r[:width]))
			w = string(r[width:])
		}
		if w == "" {
			continue
		}
		switch {
		case cur == "":
			cur = w
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
