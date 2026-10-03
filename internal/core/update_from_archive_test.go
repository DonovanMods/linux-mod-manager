package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #530: updating an installed mod from an archive the user
// downloaded by hand, because the source will not serve it through its API.

// fromFileSource is a source that lists files and advertises updates, but
// refuses every download the way CurseForge does for an author's opt-out.
type fromFileSource struct {
	*adoptTestSource
	updates  []domain.Update
	checkErr error
}

func (s *fromFileSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	for _, u := range s.updates {
		for _, inst := range installed {
			if inst.ID == u.InstalledMod.ID {
				u.InstalledMod = inst
				out = append(out, u)
			}
		}
	}
	return out, s.checkErr
}

func (s *fromFileSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", &source.ManualDownloadError{Reason: manualReason}
}

// fromFileFixture is an installed, deployed Auctionator 1.0 (file "100")
// whose source lists 1.0, its 2.0 build (file "200", advertised by the update
// check) and another flavor's 2.0 build (file "201").
type fromFileFixture struct {
	svc     *core.Service
	game    *domain.Game
	src     *fromFileSource
	gameDir string
}

func newFromFileFixture(t *testing.T) *fromFileFixture {
	t.Helper()
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"msrc": "g1"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	src := &fromFileSource{adoptTestSource: newAdoptTestSource("msrc")}
	src.mods["42"] = &domain.Mod{ID: "42", SourceID: "msrc", Name: "Auctionator", Version: "2.0", GameID: "g1",
		SourceURL: "https://example.test/mods/auctionator"}
	src.files = []domain.DownloadableFile{
		{ID: "100", FileName: "Auctionator-1.0.zip", Version: "1.0"},
		{ID: "200", FileName: "Auctionator-2.0.zip", Version: "2.0"},
		{ID: "201", FileName: "Auctionator-Classic-2.0.zip", Version: "2.0"},
	}
	src.updates = []domain.Update{{
		InstalledMod:       domain.InstalledMod{Mod: domain.Mod{ID: "42"}},
		NewVersion:         "2.0",
		FileIDReplacements: map[string]string{"100": "200"},
	}}
	svc.RegisterSource(src)

	seedUpdatableMod(t, svc, game, "msrc", "42", "Auctionator", "1.0", []string{"100"},
		map[string][]byte{"Auctionator/Auctionator-old.lua": []byte("old")})
	return &fromFileFixture{svc: svc, game: game, src: src, gameDir: gameDir}
}

// archive writes a zip named name holding the 2.0 payload.
func (f *fromFileFixture) archive(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	createImportTestZip(t, path, map[string]string{"Auctionator/Auctionator-new.lua": "new"})
	return path
}

func (f *fromFileFixture) plan(t *testing.T, archive string, opts core.UpdateFromArchiveOptions) *core.UpdateFromArchivePlan {
	t.Helper()
	plan, err := f.svc.PlanUpdateFromArchive(context.Background(), f.game, "default", "msrc", "42", archive, opts)
	require.NoError(t, err)
	return plan
}

func (f *fromFileFixture) row(t *testing.T) *domain.InstalledMod {
	t.Helper()
	row, err := f.svc.GetInstalledMod(context.Background(), "msrc", "42", "g1", "default")
	require.NoError(t, err)
	return row
}

// The archive is the file the update check advertised: its version and file
// ID are adopted, previous_* recorded, the archive's checksum stored, the new
// files deployed over the old ones, and the profile ref follows.
func TestUpdateFromArchive_AdvertisedMatch_UpdatesLikeADownload(t *testing.T) {
	f := newFromFileFixture(t)
	archive := f.archive(t, "Auctionator-2.0.zip")

	plan := f.plan(t, archive, core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchAdvertised, plan.Match)
	assert.Equal(t, "1.0", plan.FromVersion)
	assert.Equal(t, "2.0", plan.ToVersion)
	assert.Equal(t, []string{"200"}, plan.FileIDs)
	assert.Equal(t, &core.ArchiveFileRef{ID: "200", FileName: "Auctionator-2.0.zip", Version: "2.0"}, plan.Advertised)
	assert.Equal(t, []string{"Auctionator/Auctionator-new.lua"}, plan.Files)
	assert.Empty(t, plan.Warnings)
	assert.False(t, plan.Locked)

	result, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, core.UpdateUpdated, result.Status)
	assert.Equal(t, "Auctionator", result.Name)
	assert.Equal(t, "1.0", result.FromVersion)
	assert.Equal(t, "2.0", result.ToVersion)

	row := f.row(t)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, []string{"200"}, row.FileIDs)
	assert.Equal(t, "1.0", row.PreviousVersion)
	assert.Equal(t, []string{"100"}, row.PreviousFileIDs)
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy, "the update policy is kept")
	assert.True(t, row.Enabled)
	assert.Equal(t, fileMD5(t, archive), storedChecksum(t, f.svc, "g1", "msrc", "42", "200"))

	_, err = os.Lstat(filepath.Join(f.gameDir, "Auctionator", "Auctionator-old.lua"))
	assert.True(t, os.IsNotExist(err), "the old version is undeployed")
	got, err := os.ReadFile(filepath.Join(f.gameDir, "Auctionator", "Auctionator-new.lua"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))

	prof, err := f.svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := prof.FindRef("msrc", "42")
	require.NotNil(t, ref)
	assert.Equal(t, "2.0", ref.Version)
	assert.Equal(t, []string{"200"}, ref.FileIDs)

	assert.True(t, f.svc.GetGameCache(f.game).Exists("g1", "msrc", "42", "1.0"), "the old version's cache entry survives for rollback")

	verified, err := f.svc.VerifyForTest(context.Background(), f.game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	assert.Zero(t, verified.Warnings, "an updated mod verifies clean: %+v", verified.Findings)
}

// Rollback after an update from file restores the previous version.
func TestUpdateFromArchive_ThenRollback_RestoresThePreviousVersion(t *testing.T) {
	f := newFromFileFixture(t)
	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)

	rb, err := f.svc.PlanRollback(context.Background(), f.game, "default", "msrc", "42")
	require.NoError(t, err)
	assert.False(t, rb.CacheMissing)
	_, err = f.svc.ApplyRollback(context.Background(), f.game, rb, core.RollbackOptions{}, nil)
	require.NoError(t, err)

	row := f.row(t)
	assert.Equal(t, "1.0", row.Version)
	assert.Equal(t, []string{"100"}, row.FileIDs)
	_, err = os.Lstat(filepath.Join(f.gameDir, "Auctionator", "Auctionator-old.lua"))
	require.NoError(t, err, "the previous version is redeployed")
	_, err = os.Lstat(filepath.Join(f.gameDir, "Auctionator", "Auctionator-new.lua"))
	assert.True(t, os.IsNotExist(err))
}

// A browser's duplicate-download suffix does not stop the match, and the plan
// says which file it matched.
func TestUpdateFromArchive_DuplicateDownloadSuffix_StillMatches(t *testing.T) {
	f := newFromFileFixture(t)
	plan := f.plan(t, f.archive(t, "Auctionator-2.0 (1).zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchAdvertised, plan.Match)
	assert.True(t, plan.MatchNormalized)
	assert.Equal(t, "Auctionator-2.0.zip", plan.MatchedFile.FileName)
	assert.Equal(t, []string{"200"}, plan.FileIDs)
}

// Another flavor's build is refused unless the mismatch is accepted; nothing
// changes on the refusal, and the accepted update records the file the
// archive actually is.
func TestUpdateFromArchive_Mismatch_RefusedUnlessAccepted(t *testing.T) {
	f := newFromFileFixture(t)
	plan := f.plan(t, f.archive(t, "Auctionator-Classic-2.0.zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchMismatch, plan.Match)
	assert.Equal(t, "201", plan.MatchedFile.ID)
	assert.Equal(t, []string{"201"}, plan.FileIDs)
	assert.Empty(t, plan.Warnings, "the mismatch is Match's to state, not a warning's")

	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	var mismatch *core.ArchiveMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, "Auctionator-Classic-2.0.zip", mismatch.ArchiveName)
	assert.Equal(t, "200", mismatch.Advertised.ID)
	assert.Equal(t, "1.0", f.row(t).Version, "a refusal changes nothing")
	assert.False(t, f.svc.GetGameCache(f.game).Exists("g1", "msrc", "42", "2.0"), "nothing is ingested before the gate")

	_, err = f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{AcceptMismatch: true}, nil)
	require.NoError(t, err)
	row := f.row(t)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, []string{"201"}, row.FileIDs)
}

// A name the source does not list, with an update advertised, is a mismatch
// too - its version comes from the name, and no file ID is recorded.
func TestUpdateFromArchive_UnlistedName_IsAMismatchWithNoFileID(t *testing.T) {
	f := newFromFileFixture(t)
	plan := f.plan(t, f.archive(t, "auctionator-renamed-2.1.zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchMismatch, plan.Match)
	assert.Nil(t, plan.MatchedFile)
	assert.Equal(t, "2.1", plan.ToVersion)
	assert.Empty(t, plan.FileIDs)
}

// With nothing advertised, a listed file's version and ID are adopted.
func TestUpdateFromArchive_NothingAdvertised_ListedFileIsAdopted(t *testing.T) {
	f := newFromFileFixture(t)
	f.src.updates = nil
	plan := f.plan(t, f.archive(t, "Auctionator-Classic-2.0.zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchListed, plan.Match)
	assert.Nil(t, plan.Advertised)
	assert.Equal(t, "2.0", plan.ToVersion)
	assert.Equal(t, []string{"201"}, plan.FileIDs)
	assert.Empty(t, plan.Warnings)

	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"201"}, f.row(t).FileIDs)
}

// With nothing advertised or listed, the version is the name's, or the one
// given; with neither, the plan asks for it.
func TestUpdateFromArchive_NothingAdvertisedOrListed(t *testing.T) {
	f := newFromFileFixture(t)
	f.src.updates = nil

	plan := f.plan(t, f.archive(t, "my-auctionator-3.1.zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchNone, plan.Match)
	assert.Equal(t, "3.1", plan.ToVersion)
	assert.Empty(t, plan.FileIDs)
	require.Len(t, plan.Warnings, 1)
	assert.Contains(t, plan.Warnings[0], "no file ID is recorded")

	_, err := f.svc.PlanUpdateFromArchive(context.Background(), f.game, "default", "msrc", "42",
		f.archive(t, "Auctionator-339-1-g23f0261.zip"), core.UpdateFromArchiveOptions{})
	var need *core.ArchiveVersionRequiredError
	require.ErrorAs(t, err, &need)
	assert.Equal(t, "Auctionator-339-1-g23f0261.zip", need.ArchiveName)

	plan = f.plan(t, f.archive(t, "Auctionator-339-1-g23f0261.zip"), core.UpdateFromArchiveOptions{Version: "339-1"})
	assert.Equal(t, "339-1", plan.ToVersion)
}

// A failed update check is a warning, not a refusal: the listing still
// identifies the archive.
func TestUpdateFromArchive_CheckFails_Warns(t *testing.T) {
	f := newFromFileFixture(t)
	f.src.checkErr = errors.New("offline")
	f.src.updates = nil
	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchListed, plan.Match)
	require.NotEmpty(t, plan.Warnings)
	assert.Contains(t, plan.Warnings[0], "could not check msrc for an update")
}

// The installed version again is refused: the ingest would overwrite the
// live cache entry.
func TestUpdateFromArchive_SameVersion_Refused(t *testing.T) {
	f := newFromFileFixture(t)
	_, err := f.svc.PlanUpdateFromArchive(context.Background(), f.game, "default", "msrc", "42",
		f.archive(t, "Auctionator-1.0.zip"), core.UpdateFromArchiveOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already at v1.0")
}

// A locked mod plans with the lock refusal and Apply refuses it, as an
// update does (#325).
func TestUpdateFromArchive_Locked_Refused(t *testing.T) {
	f := newFromFileFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.NewProfileManager().SetModLock(ctx, "g1", "default", "msrc", "42", "1.0"))

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	assert.True(t, plan.Locked)
	assert.Contains(t, plan.Refusal, "Auctionator is locked at v1.0 in profile default")

	_, err := f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.ErrorIs(t, err, core.ErrModLocked)
	assert.Equal(t, "1.0", f.row(t).Version)
}

// An archive changed after the plan is a stale plan.
func TestUpdateFromArchive_ArchiveChanged_StalePlan(t *testing.T) {
	f := newFromFileFixture(t)
	archive := f.archive(t, "Auctionator-2.0.zip")
	plan := f.plan(t, archive, core.UpdateFromArchiveOptions{})
	createImportTestZip(t, archive, map[string]string{"Auctionator/Auctionator-new.lua": "a different, longer payload"})

	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// A failure after the ingest leaves the cache as it found it.
func TestUpdateFromArchive_HookFailure_DiscardsTheNewCacheEntry(t *testing.T) {
	f := newFromFileFixture(t)
	f.game.Hooks.Install.BeforeEach = createTestScript(t, t.TempDir(), "fail.sh", "#!/bin/sh\nexit 1\n")
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))

	plan := f.plan(t, f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	assert.Equal(t, []string{"install.before_each"}, plan.Hooks)
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, nil)
	require.ErrorContains(t, err, "install.before_each hook failed")

	assert.Equal(t, "1.0", f.row(t).Version)
	assert.False(t, f.svc.GetGameCache(f.game).Exists("g1", "msrc", "42", "2.0"), "the failed update's cache entry is removed")
	assert.True(t, f.svc.GetGameCache(f.game).Exists("g1", "msrc", "42", "1.0"))
}

// A Workshop item is Steam's to update.
func TestUpdateFromArchive_External_Refused(t *testing.T) {
	f := newFromFileFixture(t)
	row := f.row(t)
	row.External = true
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), row))

	_, err := f.svc.PlanUpdateFromArchive(context.Background(), f.game, "default", "msrc", "42",
		f.archive(t, "Auctionator-2.0.zip"), core.UpdateFromArchiveOptions{})
	require.Error(t, err)
}

// #530: a batch update whose source refuses the download says so on the
// wire, so the web UI can offer "Update from file…" beside the mod's page -
// the job result document is all it has.
func TestApplyUpdateBatch_ManualDownloadFailureSaysSo(t *testing.T) {
	f := newFromFileFixture(t)
	row := f.row(t)
	plan, err := f.svc.PlanUpdateBatchFrom(context.Background(), f.game, "default", []domain.Update{{InstalledMod: *row, NewVersion: "2.0",
		FileIDReplacements: map[string]string{"100": "200"}}}, nil)
	require.NoError(t, err)
	result, err := f.svc.ApplyUpdateBatch(context.Background(), f.game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.True(t, result.Failed[0].ManualDownload)
	assert.Equal(t, "https://example.test/mods/auctionator", result.Failed[0].ModURL)
}
