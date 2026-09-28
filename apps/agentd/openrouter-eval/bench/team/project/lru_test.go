package team

import "testing"

func TestLRU(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("c", "3")
	if _, ok := c.Get("a"); ok {
		t.Error("a should have been evicted")
	}
	if v, ok := c.Get("c"); !ok || v != "3" {
		t.Errorf("Get(c) = %q, %v", v, ok)
	}
}
