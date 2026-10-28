package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// #505: when the file an update needs is gone from the source's listing,
// selectUpdateDeployFiles used to pick a stand-in by version label alone. A
// label is not unique across a mod's file classifications (CurseForge
// publishes one label for Forge, Fabric, NeoForge and Quilt builds), so the
// pick could silently install another flavor's file. These tests pin the
// rule that replaced it:
//
//   - a file the update check ADVERTISED (a FileIDReplacements target) that
//     is no longer listed is never replaced by a guess, however many
//     same-label files exist: the source named one file, and only that file
//     is the update;
//   - an unadvertised stored file that is gone (#95's fallback) is replaced
//     only when exactly one same-label file could stand in for it - same
//     Category where the candidates carry one - and refused otherwise.

// requireTargetUnavailable asserts err is an *UpdateTargetUnavailableError
// and returns it.
func requireTargetUnavailable(t *testing.T, err error) *UpdateTargetUnavailableError {
	t.Helper()
	require.Error(t, err)
	var target *UpdateTargetUnavailableError
	require.True(t, errors.As(err, &target), "want *UpdateTargetUnavailableError, got %T: %v", err, err)
	return target
}

// The advertised Forge file is gone and two other same-label files remain:
// today the first one listed (Fabric) was installed.
func TestSelectUpdateDeployFiles_AdvertisedFileGone_SeveralSameLabelCandidates_Refuses(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "8999990", Name: "bigpack-fabric-1.20.1-1.9.0", Version: "1.9.0", IsPrimary: true, Category: "release"},
		{ID: "8999991", Name: "bigpack-quilt-1.20.1-1.9.0", Version: "1.9.0", Category: "release"},
		{ID: "8000001", Name: "bigpack-forge-1.20.1-1.0.0", Version: "1.0.0", Category: "release"},
	}
	replaced := map[string]bool{"8999930": true} // {8000001 -> 8999930}, and 8999930 is not listed

	_, _, err := selectUpdateDeployFiles(files, "1.9.0", "1.0.0", []string{"8000001"}, []string{"8999930"}, replaced)

	target := requireTargetUnavailable(t, err)
	assert.Equal(t, "1.9.0", target.TargetVersion)
	assert.Equal(t, []string{"8999930"}, target.MissingFileIDs)
	assert.True(t, target.Advertised)
	assert.Equal(t, []UpdateTargetCandidate{
		{ID: "8999990", Name: "bigpack-fabric-1.20.1-1.9.0", Version: "1.9.0", Category: "release"},
		{ID: "8999991", Name: "bigpack-quilt-1.20.1-1.9.0", Version: "1.9.0", Category: "release"},
	}, target.Candidates)
}

// #504's actual repro, before the CurseForge listing paginated: the
// advertised Forge file sat beyond the first page, and the ONLY 1.9.0 file
// the listing held was the Fabric one - a single candidate, and the wrong
// one. Candidate count cannot make a guess safe.
func TestSelectUpdateDeployFiles_AdvertisedFileGone_SingleSameLabelCandidate_Refuses(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "8999990", Name: "bigpack-fabric-1.20.1-1.9.0", Version: "1.9.0", IsPrimary: true, Category: "release"},
		{ID: "8999995", Name: "bigpack-neoforge-1.21-2.0.0", Version: "2.0.0", Category: "release"},
	}
	replaced := map[string]bool{"8999930": true}

	_, _, err := selectUpdateDeployFiles(files, "1.9.0", "1.0.0", []string{"8000001"}, []string{"8999930"}, replaced)

	target := requireTargetUnavailable(t, err)
	assert.Equal(t, []string{"8999930"}, target.MissingFileIDs)
	assert.True(t, target.Advertised)
	require.Len(t, target.Candidates, 1)
	assert.Equal(t, "8999990", target.Candidates[0].ID)
}

// An advertised file that is gone is refused even when no file carries the
// target label at all: the old path handed this to selectDeployFiles, which
// installs whichever stored files survive (or the primary) - another guess.
func TestSelectUpdateDeployFiles_AdvertisedFileGone_NoSameLabelCandidate_Refuses(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "8999995", Name: "bigpack-neoforge-1.21-2.0.0", Version: "2.0.0", IsPrimary: true, Category: "release"},
	}
	replaced := map[string]bool{"8999930": true}

	_, _, err := selectUpdateDeployFiles(files, "1.9.0", "1.0.0", []string{"8000001"}, []string{"8999930"}, replaced)

	target := requireTargetUnavailable(t, err)
	assert.Equal(t, []string{"8999930"}, target.MissingFileIDs)
	assert.Empty(t, target.Candidates)
}

// NexusMods maps one FileUpdates hop only (A -> B). If the author later
// deleted B and uploaded C under the target label, the check still advertises
// B, and the old label fallback happened to land on C. It is refused now: C
// was a guess the source never made, and with several same-label files the
// same fallback picks by list order. Re-running the check re-advertises B
// (one hop) - the user's remedy is the explicit install the error names.
func TestSelectUpdateDeployFiles_NexusOneHopChain_DeletedTarget_Refuses(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "C", Name: "Main File", Version: "2.0", IsPrimary: true, Category: "MAIN"},
		{ID: "A", Name: "Main File", Version: "1.0", Category: "OLD_VERSION"},
	}
	replaced := map[string]bool{"B": true} // {A -> B}; B was deleted

	_, _, err := selectUpdateDeployFiles(files, "2.0", "1.0", []string{"A"}, []string{"B"}, replaced)

	target := requireTargetUnavailable(t, err)
	assert.Equal(t, []string{"B"}, target.MissingFileIDs)
	assert.True(t, target.Advertised)
	assert.Equal(t, []UpdateTargetCandidate{{ID: "C", Name: "Main File", Version: "2.0", Category: "MAIN"}}, target.Candidates)
}

// #95's fallback, unadvertised: the stored file is gone and exactly one
// same-label file is listed. It still installs - today's behaviour.
func TestSelectUpdateDeployFiles_UnadvertisedFileGone_SingleCandidate_Installs(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "new-1", Name: "Main File", Version: "2.0", IsPrimary: true, Category: "MAIN"},
	}

	selected, _, err := selectUpdateDeployFiles(files, "2.0", "1.0", []string{"old-1"}, []string{"old-1"}, nil)

	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.Equal(t, "new-1", selected[0].ID)
}

// The NexusMods rebuild shape: the old main is gone and the new version
// ships a MAIN and an OPTIONAL under the same label. Only one file shares
// the stand-in's category, so the main still installs.
func TestSelectUpdateDeployFiles_UnadvertisedFileGone_OneCandidatePerCategory_Installs(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "opt-2", Name: "Optional Patch", Version: "2.0", Category: "OPTIONAL"},
		{ID: "main-2", Name: "Main File", Version: "2.0", IsPrimary: true, Category: "MAIN"},
	}

	selected, _, err := selectUpdateDeployFiles(files, "2.0", "1.0", []string{"old-main"}, []string{"old-main"}, nil)

	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.Equal(t, "main-2", selected[0].ID)
}

// Unadvertised and gone, with two same-label same-category files (Forge and
// Fabric builds, both "release"): nothing says which one stood in for the
// gone file, so it is refused rather than picked by list order.
func TestSelectUpdateDeployFiles_UnadvertisedFileGone_SeveralSameCategoryCandidates_Refuses(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "fabric-2", Name: "pack-fabric-2.0", Version: "2.0", IsPrimary: true, Category: "release"},
		{ID: "forge-2", Name: "pack-forge-2.0", Version: "2.0", Category: "RELEASE"},
	}

	_, _, err := selectUpdateDeployFiles(files, "2.0", "1.0", []string{"forge-1"}, []string{"forge-1"}, nil)

	target := requireTargetUnavailable(t, err)
	assert.Equal(t, []string{"forge-1"}, target.MissingFileIDs)
	assert.False(t, target.Advertised)
	assert.Len(t, target.Candidates, 2)
}

// Category-less candidates (custom sources never set one) cannot be told
// apart either, so several of them are refused too.
func TestSelectUpdateDeployFiles_UnadvertisedFileGone_SeveralCategoryLessCandidates_Refuses(t *testing.T) {
	files := []domain.DownloadableFile{
		{ID: "x-2", Name: "pack-x", Version: "2.0", IsPrimary: true},
		{ID: "y-2", Name: "pack-y", Version: "2.0"},
	}

	_, _, err := selectUpdateDeployFiles(files, "2.0", "1.0", []string{"x-1"}, []string{"x-1"}, nil)

	target := requireTargetUnavailable(t, err)
	assert.Len(t, target.Candidates, 2)
}

// The refusal names every candidate and the explicit install that picks one,
// so the message alone is enough to act on.
func TestUpdateTargetUnavailableError_MessageNamesTheRemedies(t *testing.T) {
	advertised := &UpdateTargetUnavailableError{
		SourceID: "curseforge", ModID: "4242", ModName: "Big Pack", Profile: "default",
		TargetVersion:  "1.9.0",
		MissingFileIDs: []string{"8999930"},
		Advertised:     true,
		Candidates: []UpdateTargetCandidate{
			{ID: "8999990", Name: "bigpack-fabric-1.20.1-1.9.0", Version: "1.9.0", Category: "release"},
		},
	}
	assert.Equal(t, `cannot update Big Pack to 1.9.0: the file the update check named (file ID 8999930) is no longer offered by the source, and lmm will not guess a replacement from the version label. Re-run 'lmm update' so the check names a file that exists, or install the file you want explicitly with 'lmm install --source curseforge --id 4242 --profile default --file <file-id>'; files listed under 1.9.0: 8999990 "bigpack-fabric-1.20.1-1.9.0"`, advertised.Error())

	unadvertised := &UpdateTargetUnavailableError{
		SourceID: "src", ModID: "mod1", ModName: "Mod One", Profile: "default",
		TargetVersion:  "2.0",
		MissingFileIDs: []string{"forge-1"},
		Candidates: []UpdateTargetCandidate{
			{ID: "fabric-2", Name: "pack-fabric-2.0", Version: "2.0"},
			{ID: "forge-2", Name: "pack-forge-2.0", Version: "2.0"},
		},
	}
	assert.Equal(t, `cannot update Mod One to 2.0: the installed file (file ID forge-1) is no longer offered by the source, and 2 files listed under 2.0 could replace it; lmm will not pick one by list order. Install the file you want explicitly with 'lmm install --source src --id mod1 --profile default --file <file-id>': fabric-2 "pack-fabric-2.0", forge-2 "pack-forge-2.0"`, unadvertised.Error())

	none := &UpdateTargetUnavailableError{
		SourceID: "src", ModID: "mod1", ModName: "Mod One", Profile: "default",
		TargetVersion: "2.0", MissingFileIDs: []string{"B"}, Advertised: true,
	}
	assert.Equal(t, `cannot update Mod One to 2.0: the file the update check named (file ID B) is no longer offered by the source, and lmm will not guess a replacement from the version label. Re-run 'lmm update' so the check names a file that exists, or install the file you want explicitly with 'lmm install --source src --id mod1 --profile default --file <file-id>'; no file is listed under 2.0`, none.Error())
}
