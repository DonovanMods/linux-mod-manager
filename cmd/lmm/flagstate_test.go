package main

import (
	"fmt"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// #581: the command tree is a package singleton, and every flag in it is
// bound to a package variable, so a test that sets one - by assignment, or by
// passing it through rootCmd - and does not put it back hands its value to
// every test after it. Within one run the order happened to hide that; under
// -count=2 the second run started from the first run's leftovers and failed.
// TestMain snapshots the tree before the run and refuses the binary if it
// differs afterwards, so a leak fails every ordinary `go test`, not only a
// repeated one.

// flagState is one flag's observable state: its value and whether it reads
// as passed.
type flagState struct {
	name    string
	value   string
	changed bool
}

// snapshotFlags records the state of every flag in cmd's tree, keyed by the
// flag itself (a persistent flag is one object however many commands
// inherit it).
func snapshotFlags(cmd *cobra.Command) map[*pflag.Flag]flagState {
	snap := map[*pflag.Flag]flagState{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		record := func(f *pflag.Flag) {
			if _, seen := snap[f]; !seen {
				snap[f] = flagState{name: c.CommandPath() + " --" + f.Name, value: f.Value.String(), changed: f.Changed}
			}
		}
		c.PersistentFlags().VisitAll(record)
		c.Flags().VisitAll(record)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)
	return snap
}

// leakedFlags names every flag whose state differs from before, ignoring
// the exempt variables. A flag cobra added during the run (help, version)
// counts only when it was left reading as passed.
func leakedFlags(before, after map[*pflag.Flag]flagState, exempt ...*pflag.Flag) []string {
	skip := map[*pflag.Flag]bool{}
	for _, f := range exempt {
		skip[f] = true
	}
	var leaks []string
	for f, now := range after {
		if skip[f] {
			continue
		}
		was, existed := before[f]
		switch {
		case !existed && now.changed:
			leaks = append(leaks, fmt.Sprintf("%s: left set to %q", now.name, now.value))
		case existed && (was.value != now.value || was.changed != now.changed):
			leaks = append(leaks, fmt.Sprintf("%s: %q (changed=%t) became %q (changed=%t)",
				now.name, was.value, was.changed, now.value, now.changed))
		}
	}
	sort.Strings(leaks)
	return leaks
}

// restoreFlags puts every flag in the command tree back as it was when it
// was called, once t finishes - the value, through the flag so its package
// variable follows, and the Changed bit a parse leaves set. A test that
// executes rootCmd, or assigns a flag's variable without setFlag, calls it
// first.
func restoreFlags(t *testing.T) {
	t.Helper()
	before := snapshotFlags(rootCmd)
	slices := map[*pflag.Flag][]string{}
	for f := range before {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			slices[f] = append([]string(nil), sv.GetSlice()...)
		}
	}
	t.Cleanup(func() {
		for f, now := range snapshotFlags(rootCmd) {
			was, existed := before[f]
			if !existed {
				// A flag cobra added during the test (help, version).
				was = flagState{value: f.DefValue}
			}
			if now.value != was.value {
				var err error
				if sv, ok := f.Value.(pflag.SliceValue); ok {
					err = sv.Replace(slices[f])
				} else {
					err = f.Value.Set(was.value)
				}
				if err != nil {
					t.Errorf("restoring %s to %q: %v", now.name, was.value, err)
				}
			}
			f.Changed = was.changed
		}
	})
}
