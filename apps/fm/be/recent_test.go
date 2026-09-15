package fm

import (
	"os"
	"path/filepath"
	"testing"

	wfs "github.com/sirmick/wash/internal/fs"
)

func TestClosingFolder(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "notes.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldFS, oldRoot, oldLaunch := fmFS, fmRoot, launchDir
	fmFS, fmRoot, launchDir = wfs.New(root), root, ""
	t.Cleanup(func() { fmFS, fmRoot, launchDir = oldFS, oldRoot, oldLaunch })

	cases := []struct {
		name, shown, want string
	}{
		{"shown folder", sub, sub},
		{"a selected file stands for its folder", file, sub},
		{"never navigated records the start folder", "", root},
		{"a folder that is gone records nothing", filepath.Join(root, "gone"), ""},
		{"outside the root records nothing", "/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := closingFolder(tc.shown); got != tc.want {
				t.Errorf("closingFolder(%q) = %q, want %q", tc.shown, got, tc.want)
			}
		})
	}
}
