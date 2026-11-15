//go:build race

package testutil

// RaceEnabled is true when the tests run with the race detector, which makes
// them about 10 times slower.
const RaceEnabled = true
