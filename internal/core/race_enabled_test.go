//go:build race

package core_test

// raceEnabled reports a -race build, where the race detector's
// instrumentation of the pure-Go SQLite makes wall-clock budgets
// meaningless (#677's timing check runs about 45x slower).
const raceEnabled = true
