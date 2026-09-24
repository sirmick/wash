package fm

import (
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

func ids(apps []openWithApp) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.AppID)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var openWithManifests = []wire.Manifest{
	{ID: "com.wash.edit", Name: "Editor", Surface: wire.SurfaceWindow, Opens: []string{".txt", ".md"}},
	{ID: "com.wash.imageview", Name: "Image Viewer", Surface: wire.SurfaceWindow, Opens: []string{"png", ".jpg"}},
	{ID: "com.wash.term", Name: "Terminal", Surface: wire.SurfaceWindow},
	{ID: "com.wash.bulk", Name: "Bulk Ops", Surface: wire.SurfaceBackground},
	{ID: "com.wash.secret", Name: "Hidden", Surface: wire.SurfaceWindow, Hidden: true, Opens: []string{".txt"}},
	{ID: "com.wash.top", Name: "Top", Surface: wire.SurfaceWindow},
}

func TestOpenersForRanksRegisteredFirst(t *testing.T) {
	reg, others := openersFor("notes.TXT", openWithManifests)
	if !eq(ids(reg), []string{"com.wash.edit"}) {
		t.Fatalf("registered = %v", ids(reg))
	}
	// The other known openers, name-sorted; background/hidden/unrelated
	// window apps never appear.
	if !eq(ids(others), []string{"com.wash.imageview", "com.wash.term"}) {
		t.Fatalf("others = %v", ids(others))
	}
}

func TestOpenersForAcceptsDotlessOpens(t *testing.T) {
	reg, _ := openersFor("photo.png", openWithManifests)
	if !eq(ids(reg), []string{"com.wash.imageview"}) {
		t.Fatalf("registered = %v", ids(reg))
	}
}

func TestOpenersForUnregisteredExtensionOffersEveryOpener(t *testing.T) {
	reg, others := openersFor("mystery.xyz", openWithManifests)
	if len(reg) != 0 {
		t.Fatalf("registered = %v, want none", ids(reg))
	}
	if !eq(ids(others), []string{"com.wash.edit", "com.wash.imageview", "com.wash.term"}) {
		t.Fatalf("others = %v", ids(others))
	}
}

func TestOpenersForFallsBackToTheStaticRoster(t *testing.T) {
	// A standalone fm binary sees no other app in the registry.
	reg, others := openersFor("a.txt", nil)
	if len(reg) != 0 {
		t.Fatalf("registered = %v, want none", ids(reg))
	}
	if !eq(ids(others), []string{"com.wash.edit", "com.wash.imageview", "com.wash.term"}) {
		t.Fatalf("others = %v", ids(others))
	}
}
