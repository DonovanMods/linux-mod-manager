package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redownloadCompilerSource wraps fakeCompilerSource with a real GetModFiles/
// GetDownloadURL implementation backed by a local HTTP server, so
// ApplyRecompile's "retained source missing -> fall back to re-download"
// leg has something genuine to redownload from.
type redownloadCompilerSource struct {
	*fakeCompilerSource
	downloadBody string
	files        []domain.DownloadableFile
	srv          *httptest.Server
}

func (s *redownloadCompilerSource) start(t *testing.T) {
	t.Helper()
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(s.downloadBody))
	}))
	t.Cleanup(s.srv.Close)
}

func (s *redownloadCompilerSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files, nil
}

func (s *redownloadCompilerSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.srv.URL, nil
}

var _ source.ModSource = (*redownloadCompilerSource)(nil)

// recompileFixture bundles what ApplyRecompile's tests need to assert on:
// the service, game, deployed file's game-dir path, and the base pak path
// (so a test can rewrite it to simulate a base-pak refresh).
type recompileFixture struct {
	svc          *core.Service
	game         *domain.Game
	deployedPath string // game.ModPath/Bear_Mount_P.pak
	basePak      string
}

// seedCompiledInstalledMod builds a DeployCompile game with an installed,
// DEPLOYED compiled mod: cache holds the compiled pak, its retained source,
// and a (possibly stale) base-index marker; the mod is installed via the
// real Installer (so redeploy assertions exercise the real linker) and
// recorded in the DB/profile like any other install. linkMethod is caller-
// controlled because a symlink deployment would trivially reflect the
// atomic cache swap on its own - a copy/hardlink deployment is what proves
// ApplyRecompile's redeploy step actually ran.
func seedCompiledInstalledMod(t *testing.T, linkMethod domain.LinkMethod, sourceID string, recordedHash string) recompileFixture {
	t.Helper()

	svc := newFlowsTestService(t)
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	game := &domain.Game{
		ID:          "icarus",
		InstallPath: installDir,
		ModPath:     t.TempDir(),
		DeployMode:  domain.DeployCompile,
		LinkMethod:  linkMethod,
		SourceIDs:   map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, "Bear_Mount_P.pak", []byte("stale-compiled-bytes")))
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), []byte("retained-exmodz-bytes")))
	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{"Bear_Mount_P.pak"}))
	if recordedHash != "" {
		require.NoError(t, cache.MarkBaseIndexHash(versionDir, fileID, recordedHash))
	}

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(im))

	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &im.Mod, "default"))

	pm := svc.NewProfileManager()
	_, cerr := pm.Create(game.ID, "default")
	require.NoError(t, cerr)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))

	return recompileFixture{svc: svc, game: game, deployedPath: filepath.Join(game.ModPath, "Bear_Mount_P.pak"), basePak: basePak}
}

// TestApplyRecompile_OfflineFromRetainedSource_Redeploys is the happy path:
// a stale compile recompiles from its retained .exmodz with no network
// access at all (no source registered), lands the fresh bytes in the cache
// under the SAME name, records the live base pak's fingerprint, and
// redeploys - proven with LinkCopy so the on-disk deployed file can only
// carry the new content if ReplaceForUpdate actually ran.
func TestApplyRecompile_OfflineFromRetainedSource_Redeploys(t *testing.T) {
	fx := seedCompiledInstalledMod(t, domain.LinkCopy, "fake-compiler", "0000000000000000000000000000000000dead")
	liveHash := basePakIndexHash(t, fx.basePak)

	compiler := &fakeCompilerSource{}
	fx.svc.RegisterSource(compiler)

	mod, err := fx.svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	result, err := fx.svc.ApplyRecompile(context.Background(), fx.game, "default", *mod, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"Bear_Mount_P.pak"}, result.Applied)
	require.Equal(t, 1, compiler.compileCalls)

	// fakeCompilerSource.Compile copies sourceFilePath's bytes through
	// unchanged - the retained source's content, proving it (not a
	// redownload) was used.
	deployedData, err := os.ReadFile(fx.deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "retained-exmodz-bytes", string(deployedData), "redeploy must reflect the freshly recompiled bytes")

	gameCache := fx.svc.GetGameCache(fx.game)
	hashes, err := gameCache.BaseIndexHashes(fx.game.ID, "fake-compiler", "bear-mount", "3.3")
	require.NoError(t, err)
	assert.Equal(t, liveHash, hashes["exmodz-file-id"], "the recompile must record the CURRENT live base pak hash")
}

// TestApplyRecompile_LockedRefRefuses mirrors
// TestApplyUpdate_LockedRefRefusesUpdate exactly (#196: lock-wins) - a
// locked mod's files must never be touched by a recompile.
func TestApplyRecompile_LockedRefRefuses(t *testing.T) {
	fx := seedCompiledInstalledMod(t, domain.LinkCopy, "fake-compiler", "0000000000000000000000000000000000dead")
	fx.svc.RegisterSource(&fakeCompilerSource{})

	pm := fx.svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(fx.game.ID, "default", "fake-compiler", "bear-mount", ""))

	mod, err := fx.svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	before, err := os.ReadFile(fx.deployedPath)
	require.NoError(t, err)

	_, err = fx.svc.ApplyRecompile(context.Background(), fx.game, "default", *mod, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrModLocked)
	assert.Contains(t, err.Error(), "locked at v")

	after, err := os.ReadFile(fx.deployedPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a locked mod's deployed files must never be touched")
}

// TestApplyRecompile_PinnedModRecompiles proves pinning does NOT block
// ApplyRecompile (#196 design point 3: pinning fixes the mod VERSION, not
// the base pak) - only ApplyUpdate/UpdateCheckable gate on UpdatePinned.
func TestApplyRecompile_PinnedModRecompiles(t *testing.T) {
	fx := seedCompiledInstalledMod(t, domain.LinkCopy, "fake-compiler", "0000000000000000000000000000000000dead")
	fx.svc.RegisterSource(&fakeCompilerSource{})

	mod, err := fx.svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)
	mod.UpdatePolicy = domain.UpdatePinned

	_, err = fx.svc.ApplyRecompile(context.Background(), fx.game, "default", *mod, nil)
	require.NoError(t, err, "ApplyRecompile itself must not gate on UpdatePolicy")
}

// TestApplyRecompile_LocalModMissingRetainedSource_FailsLoud: a pure local
// import has no remote to fall back to - a missing retained source must
// fail loud with an actionable remedy, never silently skip or fabricate
// content.
func TestApplyRecompile_LocalModMissingRetainedSource_FailsLoud(t *testing.T) {
	fx := seedCompiledInstalledMod(t, domain.LinkCopy, domain.SourceLocal, "0000000000000000000000000000000000dead")

	gameCache := fx.svc.GetGameCache(fx.game)
	retainedPath := gameCache.GetFilePath(fx.game.ID, domain.SourceLocal, "bear-mount", "3.3", cache.RetainedSourceName("exmodz-file-id"))
	require.NoError(t, os.Remove(retainedPath))

	fx.svc.RegisterSource(&fakeCompilerSource{})

	mod, err := fx.svc.GetInstalledMod(domain.SourceLocal, "bear-mount", "icarus", "default")
	require.NoError(t, err)

	_, err = fx.svc.ApplyRecompile(context.Background(), fx.game, "default", *mod, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retained compile source")
	assert.Contains(t, err.Error(), "no remote source")
}

// TestApplyRecompile_MissingRetainedSource_FallsBackToRedownload proves the
// #196 design's "fallback: re-download" leg for a mod with a REAL source
// connection (a download-compiled entry: fileID is that source's actual
// DownloadableFile.ID, so GetModFiles/GetDownloadURL can resolve it).
func TestApplyRecompile_MissingRetainedSource_FallsBackToRedownload(t *testing.T) {
	fx := seedCompiledInstalledMod(t, domain.LinkCopy, "fake-compiler", "0000000000000000000000000000000000dead")

	gameCache := fx.svc.GetGameCache(fx.game)
	retainedPath := gameCache.GetFilePath(fx.game.ID, "fake-compiler", "bear-mount", "3.3", cache.RetainedSourceName("exmodz-file-id"))
	require.NoError(t, os.Remove(retainedPath))

	compiler := &redownloadCompilerSource{
		fakeCompilerSource: &fakeCompilerSource{},
		downloadBody:       "redownloaded-exmodz-bytes",
		files:              []domain.DownloadableFile{{ID: "exmodz-file-id", FileName: "Bear_Mount.exmodz"}},
	}
	compiler.start(t)
	fx.svc.RegisterSource(compiler)

	mod, err := fx.svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	result, err := fx.svc.ApplyRecompile(context.Background(), fx.game, "default", *mod, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"Bear_Mount_P.pak"}, result.Applied)

	deployedData, err := os.ReadFile(fx.deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "redownloaded-exmodz-bytes", string(deployedData))
}

// TestApplyRecompile_NoCompiledEntries_FailsLoud: a mod with no base-index
// markers at all (never compiled) has nothing for ApplyRecompile to do -
// callers should never route such a mod here, but the gate must still fail
// loud rather than silently no-op if one slips through.
func TestApplyRecompile_NoCompiledEntries_FailsLoud(t *testing.T) {
	svc := newFlowsTestService(t)
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))
	svc.RegisterSource(&fakeCompilerSource{})

	mod := domain.InstalledMod{Mod: domain.Mod{ID: "plain-pak-mod", SourceID: "fake-compiler", Name: "Plain Pak", Version: "1.0", GameID: "icarus"}}

	_, err := svc.ApplyRecompile(context.Background(), game, "default", mod, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no compiled entries")
}
