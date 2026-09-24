//go:build !race

package loopback

// raceEnabled reports whether this binary was built with -race; see
// race_on_test.go.
const raceEnabled = false
