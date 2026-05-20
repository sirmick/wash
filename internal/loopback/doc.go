// Package loopback hosts the in-process spine test for wash.
//
// The test drives a real internal/router.Router against a real
// internal/sdk.Conn over an in-memory wiretest.PipePair, with a hand-
// written fake "shell" on a second pair. It is the official
// validation of the v0.0 wire protocol because the development
// sandbox SIGKILLs long-lived network listeners — TCP-based end-to-end
// tests cannot run there, but this loopback can.
package loopback
