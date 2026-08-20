package priv

import (
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// requestSummary is what a person reads on the approval toast
// (docs/SIDEBAR.md M4), so the asking app has to survive into it: sudo
// for the package manager is routine, sudo for something you did not
// start is the entire reason this queue exists.
func TestRequestSummary(t *testing.T) {
	cases := []struct {
		name string
		req  *Request
		want string
	}{
		{
			name: "app request names the app and the command",
			req: &Request{
				Kind:   KindSpawn,
				Sender: wire.Sender{AppID: "com.wash.packages"},
				Argv:   []string{"apt", "upgrade"},
			},
			want: "com.wash.packages: apt upgrade",
		},
		{
			// wash-sudo comes in over the CLI socket with no app id. It
			// must not render as ": rm -rf /" — the empty half is the
			// half that matters.
			name: "a CLI request names wash-sudo rather than nothing",
			req: &Request{
				Kind:   KindRunInline,
				Sender: wire.Sender{},
				Argv:   []string{"systemctl", "restart", "nginx"},
			},
			want: "wash-sudo: systemctl restart nginx",
		},
		{
			// Nothing useful to say about the command — fall back to the
			// kind so the toast still says what sort of thing is asking.
			name: "no argv falls back to the request kind",
			req: &Request{
				Kind:   KindSpawn,
				Sender: wire.Sender{AppID: "com.wash.disks"},
			},
			want: "com.wash.disks: spawn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestSummary(tc.req); got != tc.want {
				t.Errorf("requestSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}
