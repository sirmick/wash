package login

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// We can't unit-test LookupGroupGID directly without monkeypatching
// /etc/group; instead expose a parser variant that takes a path.
// Test that variant via a temp file.
func TestLookupGroupGIDParsing(t *testing.T) {
	dir := t.TempDir()
	groupFile := filepath.Join(dir, "group")
	if err := os.WriteFile(groupFile, []byte(
		"root:x:0:\n"+
			"daemon:x:1:\n"+
			"wash:x:993:wash-system\n"+
			"users:x:100:\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	gid, err := lookupGroupGIDIn(groupFile, "wash")
	if err != nil {
		t.Fatalf("lookup wash: %v", err)
	}
	if gid != 993 {
		t.Errorf("wash gid: got %d want 993", gid)
	}

	if _, err := lookupGroupGIDIn(groupFile, "no-such-group"); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("missing group: got %v want ErrGroupNotFound", err)
	}
}
