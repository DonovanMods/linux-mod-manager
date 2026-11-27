package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// #677: `lmm verify -v` says which pass a verify is in as each one starts,
// so a long run's later passes (23k deployed files on a WoW profile) are
// not a silent wait after the last version check. Only a pass's first tick
// prints; without -v nothing does.
func TestRenderVerifyEvent_VerboseSaysEachPhaseAsItStarts(t *testing.T) {
	ticks := []core.VerifyEvent{
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseVersions, Scope: core.Scope{Index: 1, Total: 16, ModName: "A"}},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseVersions, Scope: core.Scope{Index: 2, Total: 16, ModName: "B"}},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseFiles, Scope: core.Scope{Index: 1, Total: 1}},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseDeployed, Scope: core.Scope{Index: 1, Total: 23236}},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseDeployed, Scope: core.Scope{Index: 1200, Total: 23236}},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseBlocked, Scope: core.Scope{Index: 1, Total: 16}},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseRecords},
		{Kind: core.VerifyEvProgress, Phase: core.VerifyPhaseConvergence},
	}
	render := func() error {
		for _, ev := range ticks {
			renderVerifyEvent(ev)
		}
		return nil
	}

	oldVerbose := verbose
	t.Cleanup(func() { verbose = oldVerbose })

	verbose = false
	assert.Empty(t, captureStdout(t, render), "progress prints nothing without -v")

	verbose = true
	assert.Equal(t, ""+
		"  (verbose) Checking 16 mod(s) against their sources...\n"+
		"  (verbose) Checking 1 downloaded file(s) against the cache...\n"+
		"  (verbose) Checking 23236 deployed file(s)...\n"+
		"  (verbose) Checking 16 mod(s) for paths lmm could not deploy...\n"+
		"  (verbose) Checking the profile's deployment records...\n"+
		"  (verbose) Checking for stale deployed files...\n",
		captureStdout(t, render))
}
