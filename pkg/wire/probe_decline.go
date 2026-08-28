package wire

import "os"

// ExitNotAnApp is the exit status a non-app binary uses to tell the
// router's discovery probe "I am not a wash app — skip me."
//
// Discovery filters candidates by the wash- name prefix
// (internal/router.appBinPrefix), which keeps it from exec-probing a
// crowded /usr/bin but cannot tell an app from a helper: wash-router,
// wash-login, wash-launch and wash-sudo all wear the prefix too. They
// used to meet --wash-manifest with flag.Parse, which printed its whole
// usage block to stderr and exited 2; the router folded that into a
// disable reason and logged it, so every boot dumped ~100 lines of flag
// help. This code plus silence is the explicit way to decline.
//
// Deliberately NOT 2: Go's flag package already exits 2 on an unknown
// flag, so reusing it would leave the router one usage-suppression bug
// away from silently dropping a real app that failed to parse its own
// arguments. 66 is otherwise unused in this tree.
const ExitNotAnApp = 66

// IsManifestProbe reports whether args (an os.Args[1:]-style slice) is
// the router's discovery probe. Matches only the conventional leading
// position, same as the SDK's own intercept (pkg/sdk.maybePrintManifest):
// the router invokes `<binary> --wash-manifest` with no other args
// (WIRE.md §5), so a --wash-manifest appearing later in argv belongs to
// something the helper is wrapping, not to us.
func IsManifestProbe(args []string) bool {
	return len(args) >= 1 && args[0] == "--wash-manifest"
}

// DeclineManifestProbe exits ExitNotAnApp, printing nothing, when args
// is a discovery probe; it returns normally otherwise.
//
// Call it as the FIRST statement of a non-app entrypoint — ahead of flag
// parsing, logging setup, and anything that touches the network or the
// filesystem. Anything printed before the exit defeats the purpose: the
// registry only treats the exit code as a decline when stdout and stderr
// are both empty (internal/router.Probe), so a stray banner turns the
// binary back into a noisy listed-disabled row.
func DeclineManifestProbe(args []string) {
	if IsManifestProbe(args) {
		os.Exit(ExitNotAnApp)
	}
}
