package shellassets

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestLiteEmbedCoversTheBundle keeps the VM's trimmed embed honest. The
// default build takes the whole tree, so a new top-level asset (another
// font dir, a worker, an icon sheet) just works there — and would silently
// be missing from the VM, which lists entries one by one. Every top-level
// entry of the staged bundle except wallpapers/ must be in embed_lite.go,
// and every pack it ships must exist in packs.ts and have its wallpaper
// both embedded and staged.
func TestLiteEmbedCoversTheBundle(t *testing.T) {
	if _, err := os.Stat("assets/index.html"); err != nil {
		t.Skip("shell bundle not staged (make stages web/shell/dist into assets/)")
	}
	src, err := os.ReadFile("embed_lite.go")
	if err != nil {
		t.Fatal(err)
	}
	patterns := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		if rest, ok := strings.CutPrefix(line, "//go:embed "); ok {
			for _, p := range strings.Fields(rest) {
				patterns[strings.TrimPrefix(p, "all:")] = true
			}
		}
	}

	entries, err := os.ReadDir("assets")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "wallpapers" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !patterns["assets/"+e.Name()] {
			t.Errorf("assets/%s is in the shell bundle but not in embed_lite.go — the in-browser VM would ship without it", e.Name())
		}
	}

	m := regexp.MustCompile(`var Packs = \[\]string\{([^}]*)\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("embed_lite.go: no Packs list")
	}
	packsTS, err := os.ReadFile(filepath.Join("..", "..", "web", "lib", "src", "packs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range strings.Split(string(m[1]), ",") {
		id := strings.Trim(strings.TrimSpace(q), `"`)
		if id == "" {
			continue
		}
		if !strings.Contains(string(packsTS), "id: '"+id+"'") {
			t.Errorf("Packs names %q, which packs.ts does not define", id)
		}
		wp := regexp.MustCompile(`wallpaper: 'wallpapers/` + regexp.QuoteMeta(id) + `\.[a-z]+'`).Find(packsTS)
		if wp == nil {
			t.Errorf("packs.ts has no wallpapers/%s.* for pack %q", id, id)
			continue
		}
		path := "assets/" + strings.TrimSuffix(strings.TrimPrefix(string(wp), "wallpaper: '"), "'")
		if !patterns[path] {
			t.Errorf("pack %q ships but its wallpaper %s is not embedded", id, path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("pack %q: %s is not in the staged bundle", id, path)
		}
	}
}
