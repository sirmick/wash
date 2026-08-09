//go:build !multicall

package router

import "github.com/sirmick/wash/pkg/apps/registry"

// selfApp is a no-op in standalone builds — every app binary is its
// own executable; nothing is a self-symlink. The real implementation
// lives in selfapp_multicall.go and is compiled in via
// `go build -tags=multicall`. Returning nil here makes
// probeAndRegister fall through to the exec-probe path unchanged.
func selfApp(_ string) *registry.App { return nil }
