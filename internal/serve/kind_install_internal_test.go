package serve

// Direct unit tests for kind_install.go's confirm-page helpers - the level
// task-9 review Minor 4's fix is testable at without extending the shared
// install fixture (mutations_install_internal_test.go, whose pinned
// versions all collapse to a single file) just to grow a second file onto
// one version.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstallFileChoices_VersionPinTicksThePinnedPoolsPrimary is task-9
// review Minor 4's fix: with a version pin (p.Version != "") that narrows
// the candidate pool to two or more files and no submitted pick survives,
// the plan's own unpinned default (p.Plan.Files) usually shares no IDs
// with the pinned pool at all - falling back to it, the old behaviour,
// ticked nothing at all. The pinned pool's own first entry
// (ResolveVersionFiles sorts it there) must be ticked instead.
func TestInstallFileChoices_VersionPinTicksThePinnedPoolsPrimary(t *testing.T) {
	p := &pendingInstall{
		Version: "1.0",
		Plan: &core.InstallPlan{
			// The unpinned default names a file absent from the pinned
			// pool below - exactly the mismatch that ticked nothing.
			Files: []domain.DownloadableFile{{ID: "unpinned-main", Size: 999}},
		},
		Candidates: []domain.DownloadableFile{
			{ID: "pinned-main", Size: 100},
			{ID: "pinned-optional", Size: 50},
		},
	}

	files := installFileChoices(p, installApplyRequest{})

	require.Len(t, files, 2)
	assert.True(t, files[0].Selected, "the pinned pool's own primary (first entry) must be ticked")
	assert.False(t, files[1].Selected)
}

// TestInstallFileChoices_SubmittedPickInThePinnedPoolWins covers the
// non-empty case is unaffected: a pick that DOES survive the availability
// filter is honoured exactly as before, pin or no pin.
func TestInstallFileChoices_SubmittedPickInThePinnedPoolWins(t *testing.T) {
	p := &pendingInstall{
		Version: "1.0",
		Plan:    &core.InstallPlan{Files: []domain.DownloadableFile{{ID: "unpinned-main"}}},
		Candidates: []domain.DownloadableFile{
			{ID: "pinned-main"},
			{ID: "pinned-optional"},
		},
	}

	files := installFileChoices(p, installApplyRequest{FileIDs: []string{"pinned-optional"}})

	require.Len(t, files, 2)
	assert.False(t, files[0].Selected)
	assert.True(t, files[1].Selected)
}

// TestInstallDownloadBytes_VersionPinSumsTheTickedCandidates is the other
// half of the same fix: with a picker rendered under a version pin, the
// Download fact must be summed from the TICKED candidates' own sizes, not
// plan.TotalDownloadBytes - computed from the unpinned plan.Files, which
// can describe an entirely different set of files under a pin.
func TestInstallDownloadBytes_VersionPinSumsTheTickedCandidates(t *testing.T) {
	p := &pendingInstall{
		Version: "1.0",
		Plan: &core.InstallPlan{
			TotalDownloadBytes: 999999, // the unpinned total - must NOT be returned
			Files:              []domain.DownloadableFile{{ID: "unpinned-main", Size: 999999}},
		},
		Candidates: []domain.DownloadableFile{
			{ID: "pinned-main", Size: 100},
			{ID: "pinned-optional", Size: 50},
		},
	}
	files := []confirmFile{
		{ID: "pinned-main", Selected: true},
		{ID: "pinned-optional", Selected: false},
	}

	assert.EqualValues(t, 100, installDownloadBytes(p, files))
}

// TestInstallDownloadBytes_VersionPinUnknownSizeIsMinusOne matches
// PlanInstall's own "any non-positive size makes the whole total unknown"
// rule for the summed, pinned case too.
func TestInstallDownloadBytes_VersionPinUnknownSizeIsMinusOne(t *testing.T) {
	p := &pendingInstall{
		Version: "1.0",
		Plan:    &core.InstallPlan{},
		Candidates: []domain.DownloadableFile{
			{ID: "pinned-main", Size: 0},
		},
	}
	files := []confirmFile{{ID: "pinned-main", Selected: true}}

	assert.EqualValues(t, -1, installDownloadBytes(p, files))
}

// TestInstallDownloadBytes_UnpinnedStillUsesThePlanTotal is the fix's
// negative case: without a version pin, a rendered picker's default
// selection DOES share IDs with plan.Files (installFileChoices), so the
// plan's own total remains correct and untouched.
func TestInstallDownloadBytes_UnpinnedStillUsesThePlanTotal(t *testing.T) {
	p := &pendingInstall{
		Plan: &core.InstallPlan{TotalDownloadBytes: 42},
	}
	files := []confirmFile{{ID: "a", Selected: true}}

	assert.EqualValues(t, 42, installDownloadBytes(p, files))
}
