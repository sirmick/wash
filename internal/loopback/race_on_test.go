//go:build race

package loopback

// raceEnabled reports whether this binary was built with -race. Timing caps in
// this package scale by it: the detector costs 2-10x, which would otherwise eat
// the margin a latency assertion is measuring (docs/TEST_FLAKES.md B3).
const raceEnabled = true
