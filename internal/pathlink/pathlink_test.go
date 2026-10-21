package pathlink

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		in        string
		path      string
		line, col int
	}{
		{"a/b.go", "a/b.go", 0, 0},
		{"a/b.go:42", "a/b.go", 42, 0},
		{"a/b.go:42:7", "a/b.go", 42, 7},
		{"a/b.go:0", "a/b.go:0", 0, 0},
		{"a/b.go:x", "a/b.go:x", 0, 0},
		{"C:12", "C", 12, 0},
		{":12", ":12", 0, 0},
	} {
		p, l, col := Split(c.in)
		if p != c.path || l != c.line || col != c.col {
			t.Errorf("Split(%q) = %q %d %d, want %q %d %d", c.in, p, l, col, c.path, c.line, c.col)
		}
	}
}

func TestResolve(t *testing.T) {
	top := t.TempDir()
	base := filepath.Join(top, "proj")
	mk := func(rel string) {
		p := filepath.Join(top, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("proj/main.go")
	mk("proj/src/app.ts")
	mk("outside.txt")
	if err := os.Symlink(filepath.Join(top, "outside.txt"), filepath.Join(base, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "main.go"), filepath.Join(base, "alias.go")); err != nil {
		t.Fatal(err)
	}

	ok := func(tok, path string, line, col int) {
		t.Helper()
		h, found := Resolve(base, tok)
		want := Hit{Token: tok, Path: path, Line: line, Col: col, Dir: strings.HasSuffix(path, "src") || strings.HasSuffix(path, "proj")}
		if !found || h != want {
			t.Errorf("Resolve(%q) = %+v %v, want %+v", tok, h, found, want)
		}
	}
	no := func(tok string) {
		t.Helper()
		if h, found := Resolve(base, tok); found {
			t.Errorf("Resolve(%q) = %+v, want no link", tok, h)
		}
	}
	ok("main.go", filepath.Join(base, "main.go"), 0, 0)
	ok("./src/app.ts:12", filepath.Join(base, "src/app.ts"), 12, 0)
	ok(filepath.Join(base, "src/app.ts")+":3:9", filepath.Join(base, "src/app.ts"), 3, 9)
	ok("alias.go", filepath.Join(base, "alias.go"), 0, 0)
	no("missing.go")
	ok("src", filepath.Join(base, "src"), 0, 0)                                    // a folder, for the file manager
	ok(filepath.Join(top, "outside.txt"), filepath.Join(top, "outside.txt"), 0, 0) // absolute: explicit, anywhere it exists
	ok(base, base, 0, 0)                                                           // the folder itself, absolute
	no("../outside.txt")                                                           // relative, above the folder
	no("escape.txt")                                                               // a relative link that leads out
	no("src/../../outside.txt")                                                    // lexical escape
	no(filepath.Join(top, "nowhere.txt"))                                          // absolute but missing
	if _, found := Resolve("", "main.go"); found {
		t.Error("an empty base resolved a token")
	}
	if _, found := Resolve("proj", "main.go"); found {
		t.Error("a relative base resolved a token")
	}

	got := Probe(base, []string{"main.go", "nope", "src/app.ts:1"})
	want := []Hit{
		{Token: "main.go", Path: filepath.Join(base, "main.go")},
		{Token: "src/app.ts:1", Path: filepath.Join(base, "src/app.ts"), Line: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Probe = %+v, want %+v", got, want)
	}
}
