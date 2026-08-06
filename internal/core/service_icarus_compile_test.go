package core_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
	"github.com/stretchr/testify/require"
)

// writeFakeBasePak writes a real, minimal but VALID pak at path (#196: the
// compile branch now opens the base pak itself to read its footer IndexHash
// as a compile fingerprint, so a bare byte-stub file - fine when only the
// fake compiler ever touched this path - no longer parses). One tiny table
// entry is enough; its content is never asserted on by these tests.
func writeFakeBasePak(t *testing.T, path string) {
	t.Helper()
	w, err := unrealpak.Create(path)
	require.NoError(t, err)
	require.NoError(t, w.AddFile("Data/D_Fixture.json", []byte(`{"fixture":true}`)))
	require.NoError(t, w.Close())
}

// fakeCompilerSource is a minimal ModSource that also implements
// source.MergeCompiler, standing in for internal/source/icarus.Icarus
// without pulling that package into internal/core's tests — this test only
// needs to prove Service validates and retains (never compiles a per-mod
// pak) when DeployMode is DeployCompile (#197: merged-only).
type fakeCompilerSource struct {
	downloadURL   string
	compileCalls  int
	validateCalls int
	mergeWarnings []string
}

func (s *fakeCompilerSource) ID() string      { return "fake-compiler" }
func (s *fakeCompilerSource) Name() string    { return "Fake Compiler Source" }
func (s *fakeCompilerSource) AuthURL() string { return "" }
func (s *fakeCompilerSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.downloadURL, nil
}
func (s *fakeCompilerSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, source.ErrNotSupported
}

// ValidateSource implements source.MergeCompiler by confirming the archive
// exists — this test only asserts Service invoked it, not that it performs
// real .exmodz parsing (Task 1 covers that in the icarus package itself).
func (s *fakeCompilerSource) ValidateSource(sourceFilePath string) error {
	s.validateCalls++
	if _, err := os.Stat(sourceFilePath); err != nil {
		return err
	}
	return nil
}

// MergeCompile implements source.MergeCompiler by concatenating every
// source's bytes - enough for tests to distinguish "which sources were
// actually merged" without needing a real base pak table to patch.
func (s *fakeCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	s.compileCalls++
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return s.mergeWarnings, nil, os.WriteFile(outputPath, out, 0o644)
}

var (
	_ source.ModSource     = (*fakeCompilerSource)(nil)
	_ source.MergeCompiler = (*fakeCompilerSource)(nil)
)

// writeFakeBasePakWithTable is writeFakeBasePak's table-content-controlling
// variant - needed to simulate a base pak refresh (a new IndexHash) for
// staleness tests.
func writeFakeBasePakWithTable(t *testing.T, path string, tables map[string][]byte) {
	t.Helper()
	w, err := unrealpak.Create(path)
	require.NoError(t, err)
	for mountPath, data := range tables {
		require.NoError(t, w.AddFile(mountPath, data))
	}
	require.NoError(t, w.Close())
}

// failingValidateCompilerSource wraps fakeCompilerSource and always fails
// ValidateSource - simulates a corrupt/malformed downloaded .exmodz.
type failingValidateCompilerSource struct {
	*fakeCompilerSource
}

func (s *failingValidateCompilerSource) ValidateSource(sourceFilePath string) error {
	return fmt.Errorf("boom: not a valid .EXMODZ")
}

// TestDownloadMod_DeployCompile_ValidatesAndRetainsNoPerModPak proves the
// #197 merged-only ingest contract: a downloaded .exmodz is validated and
// its bytes retained, but no per-mod pak is compiled or deployed - the
// merged pak (a separate, profile-level cache entry) is what actually
// deploys.
func TestDownloadMod_DeployCompile_ValidatesAndRetainsNoPerModPak(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{downloadURL: dlSrv.URL}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bear_Mount.exmodz"}

	result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.NoError(t, err)
	require.Equal(t, 1, src.validateCalls, "ingest must validate the .exmodz")
	require.Equal(t, 0, src.compileCalls, "ingest must NOT compile a per-mod pak (#197: merged-only)")
	require.Equal(t, 0, result.FilesExtracted, "a per-mod exmodz cache entry has no deployment members under the merged-only model")

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Empty(t, files, "ListFiles must report zero deployment members - the retained source is reserved, not a member")

	retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(file.ID))
	data, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data), "the original .exmodz bytes must still be retained")
}

// TestDownloadMod_DeployCompile_MalformedExmodz_FailsLoudAtIngest proves
// validation happens at ingest time, not deferred to the next merge.
func TestDownloadMod_DeployCompile_MalformedExmodz_FailsLoudAtIngest(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-a-valid-exmodz"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &failingValidateCompilerSource{fakeCompilerSource: &fakeCompilerSource{downloadURL: dlSrv.URL}}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bad-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bad_Mount.exmodz"}

	_, err = svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.Error(t, err)

	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version), "a validation failure must leave no cache entry")
}

// pakConversionOutcomeSource wraps fakeCompilerSource so a test can script
// per-ref pak-conversion failures (#221 Task 8). Any source whose ModRef is
// in failRefs is treated as an irreconcilable pak: skipped from the merged
// output and reported via the returned failed slice, mirroring
// internal/source/icarus/merge.go's real pak-dispatch failure path (which
// also surfaces a "... - deploying raw" warning for each skipped ref).
type pakConversionOutcomeSource struct {
	*fakeCompilerSource
	failRefs map[string]string
}

func (s *pakConversionOutcomeSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	s.compileCalls++
	var out []byte
	var warnings []string
	var failed []source.MergeFailure
	for _, src := range sources {
		if reason, bad := s.failRefs[src.ModRef]; bad {
			failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: reason})
			warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %s - deploying raw", src.ModRef, reason))
			continue
		}
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return warnings, failed, os.WriteFile(outputPath, out, 0o644)
}

var _ source.MergeCompiler = (*pakConversionOutcomeSource)(nil)

// seedEnabledPakMod installs an ENABLED pak-kind mod carrying BOTH a
// retained pak (cache.RetainedSourceName(fileID)) and a deployable pak copy
// recorded as the manifest's sole member - the shape Task 9's ingest
// produces for a pak eligible for conversion (opted in by default), before
// any merge/reconcile has run.
func seedEnabledPakMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string, pakContent []byte) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), pakContent))
	member := modID + ".pak"
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, member, pakContent))
	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{member}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))
}

// TestDownloadPakRetainsAndDeploysRaw proves the #221 ingest-widening
// contract for a prebuilt .pak download: a convert-eligible pak (DeployCompile
// + ConvertPaks) takes the SAME validate+retain branch an .exmodz does, but
// ALSO stages a deployable copy of itself as the manifest's sole member - the
// raw-deploy default that holds until the first successful merge flips the
// manifest (Task 8's reconcilePakManifests). With ConvertPaks=false, the pak
// must fall through to the pre-#221 legacy copy path unchanged.
func TestDownloadPakRetainsAndDeploysRaw(t *testing.T) {
	setup := func(t *testing.T, convertPaks bool) (*core.Service, *fakeCompilerSource, *domain.Game, *domain.Mod, *domain.DownloadableFile, *core.DownloadModResult) {
		t.Helper()
		dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("fake-pak-bytes"))
		}))
		t.Cleanup(dlSrv.Close)

		installDir := t.TempDir()
		basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
		require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
		writeFakeBasePak(t, basePak)

		cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
		svc, err := core.NewService(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, svc.Close()) })

		src := &fakeCompilerSource{downloadURL: dlSrv.URL}
		svc.RegisterSource(src)

		game := &domain.Game{
			ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
			DeployMode: domain.DeployCompile, ConvertPaks: convertPaks,
		}
		require.NoError(t, svc.AddGame(game))

		mod := &domain.Mod{ID: "cool-mod", SourceID: "fake-compiler", GameID: "icarus", Version: "1.0"}
		file := &domain.DownloadableFile{ID: "pak", FileName: "CoolMod.pak"}

		result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
		require.NoError(t, err)
		return svc, src, game, mod, file, result
	}

	t.Run("ConvertPaksEnabled_RetainsAndDeploysRaw", func(t *testing.T) {
		svc, src, game, mod, file, result := setup(t, true)

		require.Equal(t, 1, src.validateCalls, "ingest must validate the pak via ValidateSource")
		require.Equal(t, 0, src.compileCalls, "ingest must NOT compile at ingest time")
		require.Equal(t, 1, result.FilesExtracted, "raw-deploy default: the deployable copy counts as the one member")

		gameCache := svc.GetGameCache(game)

		retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(file.ID))
		retainedData, err := os.ReadFile(retainedPath)
		require.NoError(t, err)
		require.Equal(t, "fake-pak-bytes", string(retainedData), "the original pak bytes must be retained")

		deployablePath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, "CoolMod.pak")
		deployableData, err := os.ReadFile(deployablePath)
		require.NoError(t, err)
		require.Equal(t, "fake-pak-bytes", string(deployableData), "a deployable copy of the raw pak must also be staged")

		manifests, err := gameCache.FileManifests(game.ID, mod.SourceID, mod.ID, mod.Version)
		require.NoError(t, err)
		require.True(t, manifests[file.ID].Recorded)
		require.Equal(t, []string{"CoolMod.pak"}, manifests[file.ID].Members, "raw-deploy default: the deployable copy is the sole member")
	})

	t.Run("ConvertPaksDisabled_LegacyPath", func(t *testing.T) {
		svc, src, game, mod, file, result := setup(t, false)

		require.Equal(t, 0, src.validateCalls, "ConvertPaks=false must never touch the MergeCompiler at all")
		require.Equal(t, 1, result.FilesExtracted)

		gameCache := svc.GetGameCache(game)
		require.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version), "cache entry must exist")

		retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(file.ID))
		_, statErr := os.Stat(retainedPath)
		require.True(t, os.IsNotExist(statErr), "legacy path: no retained source must be written")

		files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
		require.NoError(t, err)
		require.Equal(t, []string{"CoolMod.pak"}, files, "legacy path: plain copy, the pak itself is the sole member")
	})
}

// TestPakInstallThenSyncNeverDoubleApplies proves the design's key safety
// property (#221): the first-install transient - a pak deployed raw at
// ingest, then converted by the first sync - heals WITHIN that one sync
// call, never leaving both the raw pak and the merged pak deployed at once,
// and a re-run afterward is a pure no-op fast path.
func TestPakInstallThenSyncNeverDoubleApplies(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.ConvertPaks = true

	seedEnabledPakMod(t, svc, game, "fake-compiler", "coolmod", "1.0", "pak", []byte("cool-pak-bytes"))

	installer, err := svc.GetInstallerForProfile(game, "default")
	require.NoError(t, err)

	mod := &domain.Mod{ID: "coolmod", SourceID: "fake-compiler", GameID: game.ID, Version: "1.0"}
	require.NoError(t, installer.Install(context.Background(), game, mod, "default"))

	rawPath := filepath.Join(game.ModPath, "coolmod.pak")
	_, err = os.Stat(rawPath)
	require.NoError(t, err, "the raw pak must be deployed before the first sync")

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	_ = warnings

	_, err = os.Stat(rawPath)
	require.True(t, os.IsNotExist(err), "reconcile must undeploy the raw pak link once the merge converts it")

	mergedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "the merged pak must be deployed")

	// Re-run: fast path, nothing changes, raw link stays gone.
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	_, err = os.Stat(rawPath)
	require.True(t, os.IsNotExist(err), "the raw pak link must stay gone on the re-run")
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "the merged pak must remain deployed")
}

// TestSyncMergedPakReconcilesPakManifests proves the #221 crux: a
// successfully converted pak mod's cache manifest flips to members=nil
// (merged pak claims it), a failed one keeps its raw pak as the sole
// member (raw fallback), the stored fingerprint records both outcomes, and
// toggling a mod's ConvertPaks off both regenerates the fingerprint
// (membership change) and flips its manifest back to raw.
func TestSyncMergedPakReconcilesPakManifests(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.ConvertPaks = true

	pakSrc := &pakConversionOutcomeSource{
		fakeCompilerSource: &fakeCompilerSource{},
		failRefs:           map[string]string{"fake-compiler:badmod": "boom"},
	}
	svc.RegisterSource(pakSrc)

	seedEnabledPakMod(t, svc, game, "fake-compiler", "goodmod", "1.0", "pak", []byte("good-pak-bytes"))
	seedEnabledPakMod(t, svc, game, "fake-compiler", "badmod", "1.0", "pak", []byte("bad-pak-bytes"))

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)

	goodManifests, err := gameCache.FileManifests(game.ID, "fake-compiler", "goodmod", "1.0")
	require.NoError(t, err)
	require.True(t, goodManifests["pak"].Recorded)
	require.Empty(t, goodManifests["pak"].Members, "a converted pak's raw copy must be unclaimed (members=nil)")

	badManifests, err := gameCache.FileManifests(game.ID, "fake-compiler", "badmod", "1.0")
	require.NoError(t, err)
	require.True(t, badManifests["pak"].Recorded)
	require.Equal(t, []string{"badmod.pak"}, badManifests["pak"].Members, "a failed conversion must keep its raw pak deployed")

	outcomes, ok := svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	byMod := make(map[string]core.MergedFingerprintEntry, len(outcomes))
	for _, o := range outcomes {
		byMod[o.ModID] = o
	}
	require.True(t, byMod["goodmod"].Converted)
	require.Empty(t, byMod["goodmod"].FailReason)
	require.False(t, byMod["badmod"].Converted)
	require.Equal(t, "boom", byMod["badmod"].FailReason)

	foundBadmod, foundDeployingRaw := false, false
	for _, w := range warnings {
		if strings.Contains(w, "badmod") {
			foundBadmod = true
		}
		if strings.Contains(w, "deploying raw") {
			foundDeployingRaw = true
		}
	}
	require.True(t, foundBadmod, "warnings must mention the failed mod: %v", warnings)
	require.True(t, foundDeployingRaw, "warnings must explain the raw fallback: %v", warnings)

	// Toggle goodmod's per-mod opt-out: it must drop out of the merge
	// (membership change -> the fingerprint regenerates and omits it) and
	// its manifest must flip back to raw deploy.
	require.NoError(t, svc.SetModConvertPaks("fake-compiler", "goodmod", game.ID, "default", false))

	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	goodManifests, err = gameCache.FileManifests(game.ID, "fake-compiler", "goodmod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"goodmod.pak"}, goodManifests["pak"].Members, "opting out must flip goodmod back to raw deploy")

	outcomes, ok = svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	for _, o := range outcomes {
		require.NotEqual(t, "goodmod", o.ModID, "an opted-out mod must not appear in the merge fingerprint")
	}
}
