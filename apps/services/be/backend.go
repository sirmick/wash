package services

import (
	"context"
)

// Backend implements the init-system-specific half of wash-services.
// Each impl wraps one init flavour (systemd / openrc / OpenWRT
// procd, …) and is constructed only if its underlying binaries +
// runtime markers are present on the host. The interface deliberately
// stays small — `Name` for the FE banner, `List` for the catalog,
// `ActionArgv` for everything priv has to run on the user's behalf.
//
// Mirrors the shape of apps/packages/be: structured read path
// (`List`) lives in-process; mutating actions just describe an argv
// the caller routes through wash-priv so password + audit live in
// one place.
type Backend interface {
	// Name is the short identifier the FE renders in the status
	// bar ("systemd", "openrc", "procd"). Also the value of the
	// ListResp.Init field — the FE picks defaults from it.
	Name() string

	// List returns the live service catalog with status + enabled
	// state filled in. Returns nil on hard failure (the FE shows
	// an empty list with the init name still visible).
	List(ctx context.Context) ([]Service, error)

	// ActionArgv returns the argv to run via wash-priv for the
	// given (op, name) pair. Returns nil to signal "this backend
	// doesn't support this op" — the caller reports a user-visible
	// error. Supported ops conventionally include:
	//   start, stop, restart, reload, enable, disable
	// (reload is systemd-only today.)
	ActionArgv(op, name string) []string

	// LogAppID is which log viewer to spawn for the show-logs
	// deeplink. Returns "" if the backend has no canonical mapping
	// (the FE just hides the button in that case).
	LogAppID() string
}

// Detect probes each known backend in order and returns the first
// whose runtime markers + binaries are present. Returns nil if none
// match; the FE shows "no supported init system detected".
func Detect() Backend {
	if b := newSystemd(); b != nil {
		return b
	}
	if b := newOpenRC(); b != nil {
		return b
	}
	if b := newProcd(); b != nil {
		return b
	}
	return nil
}
