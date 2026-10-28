package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #535: installing a mod from an archive the user downloaded by
// hand, because its source refused the install's download - the install
// twin of #530's update from file. It is the archive import with the
// identity filled in and the file the install tried to download expected.

// installFromFileSource is fromFileSource with per-mod dependencies.
type installFromFileSource struct {
	*fromFileSource
	deps map[string][]domain.ModReference
}

func (s *installFromFileSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return s.deps[mod.ID], nil
}

// installFromFileFixture is MapUtils (mod "43"), not installed, on a source
// that refuses every download. It lists the 1.2 build (file "300", primary),
// a Classic flavor's 1.2 build ("301", labelled 1.2c) and the older 1.1
// ("299").
type installFromFileFixture struct {
	svc     *core.Service
	game    *domain.Game
	src     *installFromFileSource
	gameDir string
}

func newInstallFromFileFixture(t *testing.T) *installFromFileFixture {
	t.Helper()
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"msrc": "g1"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	src := &installFromFileSource{fromFileSource: &fromFileSource{adoptTestSource: newAdoptTestSource("msrc")}}
	src.mods["43"] = &domain.Mod{ID: "43", SourceID: "msrc", Name: "MapUtils", Version: "1.2", GameID: "g1",
		SourceURL: "https://example.test/mods/maputils"}
	src.files = []domain.DownloadableFile{
		{ID: "300", FileName: "MapUtils-1.2.zip", Version: "1.2", Category: "MAIN", IsPrimary: true},
		{ID: "301", FileName: "MapUtils-Classic-1.2.zip", Version: "1.2c", Category: "MAIN"},
		{ID: "299", FileName: "MapUtils-1.1.zip", Version: "1.1", Category: "MAIN"},
	}
	svc.RegisterSource(src)
	return &installFromFileFixture{svc: svc, game: game, src: src, gameDir: gameDir}
}

// archive writes a zip named name holding MapUtils' payload.
func (f *installFromFileFixture) archive(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	createImportTestZip(t, path, map[string]string{"MapUtils/MapUtils.lua": "maputils"})
	return path
}

func installFromFileOpts(opts core.ImportArchiveOptions) core.ImportArchiveOptions {
	opts.SourceID, opts.ModID, opts.InstallFromFile = "msrc", "43", true
	return opts
}

func (f *installFromFileFixture) plan(t *testing.T, archive string, opts core.ImportArchiveOptions) *core.ImportArchivePlan {
	t.Helper()
	plan, err := f.svc.PlanImportArchive(context.Background(), f.game, "default", archive, installFromFileOpts(opts))
	require.NoError(t, err)
	return plan
}

func (f *installFromFileFixture) apply(t *testing.T, plan *core.ImportArchivePlan, opts core.ImportArchiveOptions) (*core.ImportArchiveResult, error) {
	t.Helper()
	return f.svc.ApplyImportArchive(context.Background(), f.game, "default", plan, installFromFileOpts(opts), nil)
}

func (f *installFromFileFixture) row(t *testing.T) *domain.InstalledMod {
	t.Helper()
	row, err := f.svc.GetInstalledMod(context.Background(), "msrc", "43", "g1", "default")
	require.NoError(t, err)
	return row
}

// failedInstall runs the real install of MapUtils, which the source refuses,
// and returns the typed failure the frontends read the identity from.
func (f *installFromFileFixture) failedInstall(t *testing.T) *core.DownloadError {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanInstall(ctx, f.game, "default", "msrc", "43", false)
	require.NoError(t, err)
	_, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{}, nil)
	var dl *core.DownloadError
	require.ErrorAs(t, err, &dl)
	return dl
}

// The failed install names the file it tried to download; installing from
// the archive the browser saved under a duplicate's name matches that file,
// adopts its version and file ID, records the archive's checksum, deploys it
// and adds it to the profile - so a later update check compares file IDs.
func TestInstallFromArchive_IdentityFromTheFailure_MatchesTheFileItTried(t *testing.T) {
	f := newInstallFromFileFixture(t)
	dl := f.failedInstall(t)
	assert.True(t, dl.ManualDownload)
	assert.Equal(t, "300", dl.FileID, "the failure names the file the install tried")
	assert.Equal(t, "MapUtils-1.2.zip", dl.FileName)

	archive := f.archive(t, "MapUtils-1.2 (1).zip")
	plan := f.plan(t, archive, core.ImportArchiveOptions{ExpectedFileID: dl.FileID})
	assert.Equal(t, core.ArchiveMatchAdvertised, plan.Match)
	assert.True(t, plan.MatchNormalized)
	assert.Equal(t, &core.ArchiveFileRef{ID: "300", FileName: "MapUtils-1.2.zip", Version: "1.2"}, plan.Expected)
	assert.Equal(t, &core.ArchiveFileRef{ID: "300", FileName: "MapUtils-1.2.zip", Version: "1.2"}, plan.MatchedFile)
	assert.Equal(t, "43", plan.Mod.ID)
	assert.Equal(t, "msrc", plan.Mod.SourceID)
	assert.Equal(t, "MapUtils", plan.Mod.Name)
	assert.Equal(t, "1.2", plan.Mod.Version)
	assert.Equal(t, "msrc", plan.LinkedSource)
	assert.Empty(t, plan.Warnings)
	assert.Empty(t, plan.UnmetDependencies)

	result, err := f.apply(t, plan, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, "300", result.FileID)

	row := f.row(t)
	assert.Equal(t, "1.2", row.Version)
	assert.Equal(t, []string{"300"}, row.FileIDs)
	assert.True(t, row.Enabled)
	assert.Equal(t, fileMD5(t, archive), storedChecksum(t, f.svc, "g1", "msrc", "43", "300"))
	got, err := os.ReadFile(filepath.Join(f.gameDir, "MapUtils", "MapUtils.lua"))
	require.NoError(t, err)
	assert.Equal(t, "maputils", string(got))

	prof, err := f.svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := prof.FindRef("msrc", "43")
	require.NotNil(t, ref)
	assert.Equal(t, "1.2", ref.Version)
	assert.Equal(t, []string{"300"}, ref.FileIDs)
}

// With no file named (a CLI install typed by hand), the expected file is the
// one the install would pick: the primary file, or the one at --version.
func TestInstallFromArchive_NoFileNamed_ExpectsTheInstallsOwnPick(t *testing.T) {
	f := newInstallFromFileFixture(t)

	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{})
	assert.Equal(t, core.ArchiveMatchAdvertised, plan.Match)
	require.NotNil(t, plan.Expected)
	assert.Equal(t, "300", plan.Expected.ID, "the primary file")

	plan = f.plan(t, f.archive(t, "MapUtils-1.1.zip"), core.ImportArchiveOptions{Version: "1.1"})
	assert.Equal(t, core.ArchiveMatchAdvertised, plan.Match)
	require.NotNil(t, plan.Expected)
	assert.Equal(t, "299", plan.Expected.ID, "--version picks the file at that version")
	assert.Equal(t, "1.1", plan.Mod.Version)
}

// Another flavor's build is not the file the install tried: Apply refuses it
// - nothing installed, nothing cached - until the mismatch is accepted, and
// then records the file the archive actually is.
func TestInstallFromArchive_Mismatch_RefusedUntilAccepted(t *testing.T) {
	f := newInstallFromFileFixture(t)
	archive := f.archive(t, "MapUtils-Classic-1.2.zip")

	plan := f.plan(t, archive, core.ImportArchiveOptions{ExpectedFileID: "300"})
	assert.Equal(t, core.ArchiveMatchMismatch, plan.Match)
	assert.Equal(t, &core.ArchiveFileRef{ID: "301", FileName: "MapUtils-Classic-1.2.zip", Version: "1.2c"}, plan.MatchedFile)
	assert.Equal(t, "1.2c", plan.Mod.Version, "what an accepted mismatch would record")

	_, err := f.apply(t, plan, core.ImportArchiveOptions{ExpectedFileID: "300"})
	var mismatch *core.ArchiveMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.True(t, mismatch.Install)
	assert.Equal(t, "MapUtils-1.2.zip", mismatch.Advertised.FileName)
	assert.Contains(t, err.Error(), "not the file lmm would install (MapUtils-1.2.zip)")
	assert.Contains(t, err.Error(), "install from it anyway with --accept-mismatch")
	_, err = f.svc.GetInstalledMod(context.Background(), "msrc", "43", "g1", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound, "a refusal installs nothing")
	assert.False(t, f.svc.GetGameCache(f.game).Exists("g1", "msrc", "43", "1.2c"), "and caches nothing")

	plan = f.plan(t, archive, core.ImportArchiveOptions{ExpectedFileID: "300"})
	_, err = f.apply(t, plan, core.ImportArchiveOptions{ExpectedFileID: "300", AcceptMismatch: true})
	require.NoError(t, err)
	row := f.row(t)
	assert.Equal(t, "1.2c", row.Version)
	assert.Equal(t, []string{"301"}, row.FileIDs)
}

// A name the source does not list is a mismatch too, with no matched file.
func TestInstallFromArchive_UnlistedName_IsAMismatchWithNoMatchedFile(t *testing.T) {
	f := newInstallFromFileFixture(t)

	plan := f.plan(t, f.archive(t, "maputils_v1.3.zip"), core.ImportArchiveOptions{ExpectedFileID: "300"})
	assert.Equal(t, core.ArchiveMatchMismatch, plan.Match)
	assert.Nil(t, plan.MatchedFile)
	assert.Equal(t, "1.3", plan.Mod.Version, "the name's own version")
}

// With nothing to match against (the source's file listing failed), the
// version comes from the archive's name and no file ID is recorded.
func TestInstallFromArchive_NoMatch_VersionFromTheName(t *testing.T) {
	f := newInstallFromFileFixture(t)
	f.src.filesErr = errors.New("rate limited")

	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{ExpectedFileID: "300"})
	assert.Equal(t, core.ArchiveMatchNone, plan.Match)
	assert.Nil(t, plan.Expected)
	assert.Equal(t, "1.2", plan.Mod.Version)
	assert.Contains(t, plan.Warnings, "could not fetch MapUtils's files from msrc: rate limited")
	assert.Contains(t, plan.Warnings, "MapUtils-1.2.zip is no file the source lists, so no file ID is recorded: future update checks compare versions only")

	_, err := f.apply(t, plan, core.ImportArchiveOptions{ExpectedFileID: "300"})
	require.NoError(t, err)
	assert.Empty(t, f.row(t).FileIDs)
	assert.Equal(t, "1.2", f.row(t).Version)
}

// Nothing names the version - no listed file, no version in the name - so
// the plan asks for one; --version answers it.
func TestInstallFromArchive_VersionRequired(t *testing.T) {
	f := newInstallFromFileFixture(t)
	f.src.filesErr = errors.New("rate limited")
	archive := f.archive(t, "download.zip")

	_, err := f.svc.PlanImportArchive(context.Background(), f.game, "default", archive, installFromFileOpts(core.ImportArchiveOptions{}))
	var required *core.ArchiveVersionRequiredError
	require.ErrorAs(t, err, &required)
	assert.Equal(t, "download.zip", required.ArchiveName)
	assert.Equal(t, "msrc", required.SourceID)
	assert.Equal(t, "43", required.ModID)

	plan := f.plan(t, archive, core.ImportArchiveOptions{Version: "1.4"})
	assert.Equal(t, "1.4", plan.Mod.Version)
}

// An installed mod is updated from a file, not installed from one.
func TestInstallFromArchive_AlreadyInstalled_PointsAtUpdateFromFile(t *testing.T) {
	f := newInstallFromFileFixture(t)
	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{})
	_, err := f.apply(t, plan, core.ImportArchiveOptions{})
	require.NoError(t, err)

	_, err = f.svc.PlanImportArchive(context.Background(), f.game, "default", f.archive(t, "MapUtils-1.2.zip"), installFromFileOpts(core.ImportArchiveOptions{}))
	require.ErrorIs(t, err, core.ErrArchiveModInstalled)
	assert.Contains(t, err.Error(), "lmm update 43 --from-file")
}

// Installing from a file installs that one mod: the dependencies it needs
// that are not installed are named on the plan, with what to install them
// by, rather than left out silently.
func TestInstallFromArchive_UnmetDependenciesAreNamed(t *testing.T) {
	f := newInstallFromFileFixture(t)
	f.src.mods["50"] = &domain.Mod{ID: "50", SourceID: "msrc", Name: "LibMaps", Version: "3.0", GameID: "g1"}
	f.src.deps = map[string][]domain.ModReference{"43": {{SourceID: "msrc", ModID: "50"}, {SourceID: "msrc", ModID: "404"}}}

	plan := f.plan(t, f.archive(t, "MapUtils-1.2.zip"), core.ImportArchiveOptions{})
	assert.Equal(t, []core.UnmetDependency{
		{SourceID: "msrc", ModID: "50", Name: "LibMaps"},
		{SourceID: "msrc", ModID: "404"},
	}, plan.UnmetDependencies)
	assert.Contains(t, plan.Warnings, "MapUtils depends on 2 mods that are not installed, and installing from a file does not install them: LibMaps, msrc:404")
}

// The identity is required: install from a file names the mod it installs.
func TestInstallFromArchive_RequiresTheModsIdentity(t *testing.T) {
	f := newInstallFromFileFixture(t)
	_, err := f.svc.PlanImportArchive(context.Background(), f.game, "default", f.archive(t, "MapUtils-1.2.zip"),
		core.ImportArchiveOptions{InstallFromFile: true, ModID: "43"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")

	_, err = f.svc.PlanImportArchive(context.Background(), f.game, "default", f.archive(t, "MapUtils-1.2.zip"),
		core.ImportArchiveOptions{InstallFromFile: true, SourceID: "other", ModID: "43"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured for this game")
}
