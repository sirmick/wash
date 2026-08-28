package wire

import "testing"

func TestIsManifestProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"the probe", []string{"--wash-manifest"}, true},
		{"no args", nil, false},
		{"other flag", []string{"--version"}, false},
		// wash-sudo wraps arbitrary commands: a --wash-manifest past the
		// leading position belongs to the wrapped program, and swallowing
		// it would silently refuse to run the user's command.
		{"wrapped command's flag", []string{"somecmd", "--wash-manifest"}, false},
		{"after a separator", []string{"--", "--wash-manifest"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsManifestProbe(tc.args); got != tc.want {
				t.Fatalf("IsManifestProbe(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// Reusing flag.ErrHelp's exit status would leave the router one
// usage-suppression bug away from silently dropping a real app.
func TestExitNotAnAppIsNotTheFlagErrorCode(t *testing.T) {
	if ExitNotAnApp == 2 {
		t.Fatal("ExitNotAnApp must not collide with Go's flag-parse exit code")
	}
}
