package edit

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func seedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("main.go", "package main")
	mk("README.md", "# hi")
	mk("src/app.ts", "")
	mk("src/util/paths.ts", "")
	mk(".git/HEAD", "ref")
	mk(".hidden/secret.txt", "")
	mk("node_modules/dep/index.js", "")
	mk("src/.cache/x", "")
	return root
}

func TestWalkFilesSkipsHiddenAndDependencyDirs(t *testing.T) {
	root := seedTree(t)
	files, truncated := walkFiles(context.Background(), root, 100)
	want := []string{"README.md", "main.go", "src/app.ts", "src/util/paths.ts"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	if truncated {
		t.Fatal("truncated on a tree under the limit")
	}
}

func TestWalkFilesHonoursLimitAndCancel(t *testing.T) {
	root := seedTree(t)
	files, truncated := walkFiles(context.Background(), root, 2)
	if len(files) != 2 || !truncated {
		t.Fatalf("limit: files=%v truncated=%v", files, truncated)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files, _ = walkFiles(ctx, root, 100)
	if len(files) != 0 {
		t.Fatalf("cancelled walk listed %v", files)
	}
}

func TestPushRecent(t *testing.T) {
	got := pushRecent([]string{"/a", "/b"}, "/b")
	if !reflect.DeepEqual(got, []string{"/b", "/a"}) {
		t.Fatalf("dedup: %v", got)
	}
	var many []string
	for i := 0; i < recentCap+5; i++ {
		many = append(many, string(rune('a'+i)))
	}
	got = pushRecent(many, "/new")
	if len(got) != recentCap || got[0] != "/new" {
		t.Fatalf("cap: len=%d first=%q", len(got), got[0])
	}
	if got := dropRecent([]string{"/a", "/b"}, "/a"); !reflect.DeepEqual(got, []string{"/b"}) {
		t.Fatalf("drop: %v", got)
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := loadPrefs(); len(got) != 0 {
		t.Fatalf("fresh prefs = %v", got)
	}
	if err := storePrefs(map[string]any{"font_size": 15.0, "recent": []string{"/x"}}); err != nil {
		t.Fatal(err)
	}
	p := loadPrefs()
	if p["font_size"] != 15.0 {
		t.Fatalf("font_size = %v", p["font_size"])
	}
	if got := recentOf(p); !reflect.DeepEqual(got, []string{"/x"}) {
		t.Fatalf("recent = %v", got)
	}
	// A corrupt file reads as empty rather than wedging the editor.
	if err := os.WriteFile(prefsPath(), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(); len(got) != 0 {
		t.Fatalf("corrupt prefs = %v", got)
	}
}
