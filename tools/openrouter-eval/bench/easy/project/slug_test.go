package easy

import "testing"

func TestSlug(t *testing.T) {
	if got := Slug("Hello, World!"); got != "hello-world" {
		t.Errorf("Slug = %q, want %q", got, "hello-world")
	}
}
