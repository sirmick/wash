package login

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParsePasswd(t *testing.T) {
	fixture := []byte(`root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
sync:x:4:65534:sync:/bin:/bin/sync
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
mick:x:1000:1000:Mick Sweeney,Engineering,,:/home/mick:/bin/bash
alice:x:1001:1001:Alice Example:/home/alice:/usr/bin/zsh
dave:x:1002:1002::/home/dave:/bin/false
postgres:x:118:128:PostgreSQL:/var/lib/postgresql:/bin/bash
`)
	skip := map[string]bool{"nologin": true, "false": true, "sync": true}
	users := parsePasswd(fixture, 1000, 60000, skip)
	wantNames := []string{"mick", "alice"} // input order before sort
	_ = wantNames

	// PasswdLister sorts; parsePasswd does not. Sort here so the
	// assertion is independent of internal order.
	gotNames := names(users)
	want := map[string]bool{"alice": true, "mick": true}
	if len(gotNames) != len(want) {
		t.Fatalf("got %d users (%v), want 2 (alice + mick)", len(gotNames), gotNames)
	}
	for _, n := range gotNames {
		if !want[n] {
			t.Errorf("unexpected user %q in result", n)
		}
	}

	// Look up known fields by name.
	byName := map[string]User{}
	for _, u := range users {
		byName[u.Name] = u
	}
	mick := byName["mick"]
	if mick.UID != 1000 || mick.GID != 1000 || mick.Display != "Mick Sweeney" || mick.Shell != "/bin/bash" || mick.Home != "/home/mick" {
		t.Errorf("mick fields wrong: %+v", mick)
	}
	alice := byName["alice"]
	if alice.UID != 1001 || alice.Display != "Alice Example" || alice.Shell != "/usr/bin/zsh" {
		t.Errorf("alice fields wrong: %+v", alice)
	}
}

func TestParsePasswdNoSystemUsers(t *testing.T) {
	fixture := []byte(`root:x:0:0:root:/root:/bin/bash
bin:x:2:2:bin:/bin:/usr/sbin/nologin
sshd:x:115:65534:sshd:/run/sshd:/usr/sbin/nologin
`)
	users := parsePasswd(fixture, 1000, 60000, map[string]bool{})
	if len(users) != 0 {
		t.Errorf("expected no users below uid 1000 to be listed; got %v", users)
	}
}

func TestDisplayNameFromGECOS(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Mick Sweeney,Engineering,,", "Mick Sweeney"},
		{"Alice Example", "Alice Example"},
		{",,,", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := displayNameFromGECOS(tc.in); got != tc.want {
			t.Errorf("displayNameFromGECOS(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPasswdListerListSorted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passwd")
	if err := os.WriteFile(path, []byte(
		"zzz:x:1003:1003:Z User:/home/zzz:/bin/bash\n"+
			"aaa:x:1001:1001:A User:/home/aaa:/bin/bash\n"+
			"mmm:x:1002:1002:M User:/home/mmm:/bin/bash\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &PasswdLister{Path: path}
	users, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	got := names(users)
	want := []string{"aaa", "mmm", "zzz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v (sorted)", got, want)
	}
}

func TestPasswdListerLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passwd")
	// Include a system user the regular List filter would hide,
	// to verify Lookup ignores the min-uid filter.
	if err := os.WriteFile(path, []byte(
		"root:x:0:0:root:/root:/bin/bash\n"+
			"mick:x:1000:1000:Mick:/home/mick:/bin/bash\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &PasswdLister{Path: path}

	got, err := l.Lookup("mick")
	if err != nil {
		t.Fatalf("lookup mick: %v", err)
	}
	if got.UID != 1000 || got.Home != "/home/mick" {
		t.Errorf("mick fields wrong: %+v", got)
	}

	got, err = l.Lookup("root")
	if err != nil {
		t.Fatalf("lookup root: %v", err)
	}
	if got.UID != 0 {
		t.Errorf("root uid wrong: %+v", got)
	}

	if _, err := l.Lookup("ghost"); err != ErrUserNotFound {
		t.Errorf("ghost lookup: got %v want ErrUserNotFound", err)
	}
}

func names(users []User) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.Name
	}
	return out
}
