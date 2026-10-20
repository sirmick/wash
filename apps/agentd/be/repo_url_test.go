package agentd

import "testing"

func TestWebURLFromRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/sirmick/redoubt/":         "https://github.com/sirmick/redoubt",
		"https://github.com/sirmick/redoubt.git":      "https://github.com/sirmick/redoubt",
		"https://user:tok@gitlab.example.com/a/b.git": "https://gitlab.example.com/a/b",
		"git@github.com:sirmick/redoubt.git":          "https://github.com/sirmick/redoubt",
		"ssh://git@github.com:22/sirmick/redoubt.git": "https://github.com/sirmick/redoubt",
		"/srv/git/redoubt.git":                        "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := webURLFromRemote(in); got != want {
			t.Errorf("webURLFromRemote(%q) = %q, want %q", in, got, want)
		}
	}
}
