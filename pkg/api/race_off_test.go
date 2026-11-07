//go:build !race

package api

// raceEnabled reports whether the tests were built with -race. The race
// detector makes code several times slower, so wall-clock limits are skipped.
const raceEnabled = false
