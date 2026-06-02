package priv

import (
	"strings"
	"testing"
)

func TestSudoArgv(t *testing.T) {
	tests := []struct {
		name           string
		injectPassword bool
		preserve       []string
		extraEnv       map[string]string
		target         []string
		wantMode       string // leading flag(s)
		wantPreserve   string // sorted comma list after --preserve-env=
		wantTail       []string
	}{
		{
			name:           "password mode uses -S -k",
			injectPassword: true,
			preserve:       inlinePreserveEnv,
			target:         []string{"apt", "update"},
			wantMode:       "-S",
			wantPreserve:   "WASH_BIN_DIR,WASH_CONTROL_SOCKET,WASH_DISPLAY,WASH_PROTO",
			wantTail:       []string{"--", "apt", "update"},
		},
		{
			name:           "passwordless mode uses -n",
			injectPassword: false,
			preserve:       appSpawnPreserveEnv,
			target:         []string{"/usr/bin/wash-fm"},
			wantMode:       "-n",
			wantPreserve:   "WASH_APP_ID,WASH_ATTACH_TOKEN,WASH_DISPLAY,WASH_INSTANCE_ID,WASH_PROTO",
			wantTail:       []string{"--", "/usr/bin/wash-fm"},
		},
		{
			name:           "extra env is merged and the union sorted",
			injectPassword: true,
			preserve:       inlinePreserveEnv,
			extraEnv:       map[string]string{"FOO": "1", "AAA": "2"},
			target:         []string{"sh", "-c", "echo hi"},
			wantMode:       "-S",
			wantPreserve:   "AAA,FOO,WASH_BIN_DIR,WASH_CONTROL_SOCKET,WASH_DISPLAY,WASH_PROTO",
			wantTail:       []string{"--", "sh", "-c", "echo hi"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sudoArgv(tc.injectPassword, tc.preserve, tc.extraEnv, tc.target)

			if got[0] != tc.wantMode {
				t.Fatalf("mode flag = %q, want %q (full: %v)", got[0], tc.wantMode, got)
			}

			var preserveArg string
			for _, a := range got {
				if strings.HasPrefix(a, "--preserve-env=") {
					preserveArg = strings.TrimPrefix(a, "--preserve-env=")
				}
			}
			if preserveArg != tc.wantPreserve {
				t.Errorf("preserve list = %q, want %q", preserveArg, tc.wantPreserve)
			}

			// The target (after "--") must be the final, unmodified tail.
			tail := got[len(got)-len(tc.wantTail):]
			for i := range tc.wantTail {
				if tail[i] != tc.wantTail[i] {
					t.Errorf("tail[%d] = %q, want %q (full: %v)", i, tail[i], tc.wantTail[i], got)
				}
			}
		})
	}
}
