package bulkops

import "testing"

func TestCopyNameNumbering(t *testing.T) {
	cases := []struct {
		name  string
		isDir bool
		n     int
		want  string
	}{
		{"notes.txt", false, 1, "notes (copy).txt"},
		{"notes.txt", false, 2, "notes (copy 2).txt"},
		{"notes.txt", false, 11, "notes (copy 11).txt"},
		{"notes.txt", false, 0, "notes (copy).txt"}, // n<1 clamps
		{"README", false, 1, "README (copy)"},
		{".bashrc", false, 1, ".bashrc (copy)"},
		{"archive.tar.gz", false, 1, "archive (copy).tar.gz"},
		{"archive.TAR.GZ", false, 2, "archive (copy 2).TAR.GZ"},
		{"photos", true, 1, "photos (copy)"},
		{"my.stuff", true, 1, "my.stuff (copy)"}, // a dir has no extension
	}
	for _, c := range cases {
		if got := CopyName(c.name, c.isDir, c.n); got != c.want {
			t.Errorf("CopyName(%q, %v, %d) = %q, want %q", c.name, c.isDir, c.n, got, c.want)
		}
	}
}

func TestCopyNameDoesNotStackTheSuffix(t *testing.T) {
	cases := []struct {
		name, want string
	}{
		{"notes (copy).txt", "notes (copy 2).txt"},
		{"notes (copy 3).txt", "notes (copy 2).txt"}, // n decides, not the input
		{"photos (copy)", "photos (copy 2)"},
		// Not our suffix — left alone.
		{"notes (copyright).txt", "notes (copyright) (copy 2).txt"},
		{"(copy).txt", "(copy) (copy 2).txt"},
	}
	for _, c := range cases {
		isDir := c.name == "photos (copy)"
		if got := CopyName(c.name, isDir, 2); got != c.want {
			t.Errorf("CopyName(%q, %v, 2) = %q, want %q", c.name, isDir, got, c.want)
		}
	}
}

func TestUniqueCopyNameCountsPastTakenNames(t *testing.T) {
	taken := map[string]bool{
		"notes (copy).txt":   true,
		"notes (copy 2).txt": true,
	}
	got := UniqueCopyName("notes.txt", false, func(n string) bool { return taken[n] })
	if got != "notes (copy 3).txt" {
		t.Fatalf("UniqueCopyName = %q, want notes (copy 3).txt", got)
	}
	// Duplicating the copy itself lands in the same sequence rather than
	// producing "notes (copy) (copy).txt".
	got = UniqueCopyName("notes (copy).txt", false, func(n string) bool { return taken[n] })
	if got != "notes (copy 3).txt" {
		t.Fatalf("UniqueCopyName(copy) = %q, want notes (copy 3).txt", got)
	}
}

func TestUniqueCopyNameWithNothingTaken(t *testing.T) {
	if got := UniqueCopyName("photos", true, nil); got != "photos (copy)" {
		t.Fatalf("UniqueCopyName = %q", got)
	}
	if got := UniqueCopyName("a.txt", false, func(string) bool { return false }); got != "a (copy).txt" {
		t.Fatalf("UniqueCopyName = %q", got)
	}
}
