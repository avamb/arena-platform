//go:build !race

package richtext

// raceSlowdown scales the time budgets of the pathological-input tests
// under the race detector.
const raceSlowdown = 1
