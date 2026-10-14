//go:build !race

package promptfixture

// raceEnabled is false in an ordinary build; see underRace.
const raceEnabled = false
