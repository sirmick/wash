package launch

import (
	"reflect"
	"testing"
)

// A colon does not make a URL. `wash open ./a:b` and `wash open C:file`
// are paths, and handing either to a browser would be a silent no-op.
func TestIsURL(t *testing.T) {
	for _, s := range []string{"http://x/y", "https://x", "ftp://x", "mailto:a@b"} {
		if !isURL(s) {
			t.Errorf("isURL(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"/etc/hosts", "./a:b", "C:file", "notes.md", "", "x://y"} {
		if isURL(s) {
			t.Errorf("isURL(%q) = true, want false", s)
		}
	}
}

func TestBrowserArgv(t *testing.T) {
	cases := []struct {
		name    string
		browser string
		want    []string
	}{
		{"bare command", "firefox", []string{"firefox", "http://x/"}},
		{"command with flags", "firefox --new-tab", []string{"firefox", "--new-tab", "http://x/"}},
		// The placeholder form must SUBSTITUTE, not append: appending would
		// open the browser's home page and drop the link.
		{"placeholder", "firefox %s", []string{"firefox", "http://x/"}},
		{"placeholder mid-arg", "chromium --app=%s", []string{"chromium", "--app=http://x/"}},
		{"empty", "   ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := browserArgv(c.browser, "http://x/")
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("browserArgv(%q) = %q, want %q", c.browser, got, c.want)
			}
		})
	}
}

// handlerFor answers from the registry, which is empty in a plain unit
// test binary — the useful invariant here is that an extension-less path
// never resolves to anything, since that is the case a wrong answer would
// send to the wrong app.
func TestHandlerForNoExtension(t *testing.T) {
	if got := handlerFor("/usr/bin/env"); got != "" {
		t.Fatalf("handlerFor(no extension) = %q, want \"\"", got)
	}
}
