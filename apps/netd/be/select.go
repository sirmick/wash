package netd

import "github.com/sirmick/wash/apps/netd/be/backendsel"

// Backend names + the selection decision live in apps/netd/be/backendsel, the
// one place netd and the washnet-* CLIs share so autodetect never diverges.
// These aliases keep the netd-internal call sites (and network.json values)
// reading as before.
const (
	BackendAuto     = backendsel.Auto
	BackendNM       = backendsel.NM
	BackendNetworkd = backendsel.Networkd
	BackendNetplan  = backendsel.Netplan
	BackendIfupdown = backendsel.Ifupdown
	BackendFake     = backendsel.Fake
)

type detections = backendsel.Detections

// chooseBackend delegates to backendsel.Choose (kept as a thin wrapper so the
// exhaustive selection tests stay in this package).
func chooseBackend(mode string, d detections) (string, string) { return backendsel.Choose(mode, d) }
