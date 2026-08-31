package serve

// C1 (epic live review, .superpowers/sdd/2026-08-30-serve-impl/epic-live-
// review.md): /health's repair ran at core.VerifyLocal, so its version pass
// (the network-touching phase that detects a version_mismatch and corrects
// the recorded version FIRST) never ran. perFileWalk's "missing" repair then
// re-downloaded the file the source CURRENTLY reports and stored it under
// the STILL-recorded (stale) version's cache directory - silently writing a
// newer version's content into an older version's slot while the DB kept
// claiming the old version was intact. `lmm verify --fix` never has this bug
// because it always runs at core.VerifyFull, so the version record is
// corrected before perFileWalk ever looks at the cache.
//
// This fixture reproduces the review's exact repro: a mod recorded at
// "2.0.0" with no cache dir for that version at all (simulating "the cache
// dir was deleted"), while the source's current listing for the same file ID
// reports "3.0.0" with different content ("sash v3 POISON", straight out of
// the review's own transcript).

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	versionRepairModID      = "sash"
	versionRepairFileID     = "f1"
	versionRepairMember     = "sash.txt"
	versionRepairRecorded   = "2.0.0"
	versionRepairEffective  = "3.0.0"
	versionRepairPoisonText = "sash v3 POISON"
)

// versionRepairSource is a source.ModSource with a working download,
// reporting exactly ONE file (versionRepairFileID) whose CURRENT version is
// versionRepairEffective - the state a source has moved to after the
// installed row was recorded at the OLDER versionRepairRecorded.
type versionRepairSource struct {
	server *httptest.Server
}

func newVersionRepairSource(t *testing.T) *versionRepairSource {
	t.Helper()
	s := &versionRepairSource{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipWith(versionRepairMember, versionRepairPoisonText))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (*versionRepairSource) ID() string      { return fixtureSourceID }
func (*versionRepairSource) Name() string    { return "Version Repair Fixture Source" }
func (*versionRepairSource) AuthURL() string { return "" }

func (*versionRepairSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (*versionRepairSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

func (*versionRepairSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if modID != versionRepairModID {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: versionRepairModID, SourceID: fixtureSourceID, Name: "Scarlet Sash", Version: versionRepairEffective, GameID: "g1"}, nil
}

func (*versionRepairSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

// GetModFiles always answers with the file's CURRENT version - the source's
// present-day catalog, unaware of what any installed row happens to record.
func (*versionRepairSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID != versionRepairModID {
		return nil, domain.ErrModNotFound
	}
	return []domain.DownloadableFile{
		{ID: versionRepairFileID, Name: "Main", FileName: "sash.zip", Version: versionRepairEffective, Category: "MAIN", IsPrimary: true, Size: 32},
	}, nil
}

func (s *versionRepairSource) GetDownloadURL(_ context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.server.URL + "/" + mod.ID + "/" + fileID, nil
}

func (*versionRepairSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

var _ source.ModSource = (*versionRepairSource)(nil)

// newVersionMismatchFixtureServer builds a Server whose sole installed mod
// ("sash") is recorded at versionRepairRecorded while the registered
// source's current listing for its one file reports versionRepairEffective,
// and whose cache holds NEITHER version's directory - "the cache dir was
// deleted" from the review's own repro. The mod is enabled but never
// deployed: this bug lives entirely in the cache/DB pairing, not in the
// deployed tree.
func newVersionMismatchFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	sandboxEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newVersionRepairSource(t)
	svc.RegisterSource(src)

	ctx := t.Context()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{fixtureSourceID: ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	mod := domain.Mod{ID: versionRepairModID, SourceID: fixtureSourceID, Name: "Scarlet Sash", Version: versionRepairRecorded, GameID: game.ID}
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          mod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{versionRepairFileID},
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{
		SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: []string{versionRepairFileID},
	}))

	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game
}

// TestServer_HealthFix_RepairsVersionMismatch_NotJustTheMissingFile is C1's
// headline RED test. Before the fix (pages_health.go/kind_verify_fix.go
// pinned to core.VerifyLocal), the repair "succeeds" by writing the
// source's CURRENT (3.0.0) content into the cache slot for the STALE
// recorded (2.0.0) version, leaving the DB claiming 2.0.0 is intact and a
// clean bill of health - exactly the review's reproduction. After the fix
// (core.VerifyFull, matching the CLI and /api/v1/health), the version
// record is corrected FIRST, so the redownload lands in the correct 3.0.0
// slot with a matching DB record, and a fresh full-tier verify immediately
// afterward agrees nothing is left to fix.
func TestServer_HealthFix_RepairsVersionMismatch_NotJustTheMissingFile(t *testing.T) {
	s, svc, game := newVersionMismatchFixtureServer(t)

	entry := postForm(s, "/health/fix", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code, entry.Body.String())

	rec := postForm(s, "/health/fix", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	ctx := context.Background()

	// The DB record must have moved to the version the source actually
	// reports - not stayed pinned at the stale recorded value.
	installed, err := svc.GetInstalledMod(ctx, fixtureSourceID, versionRepairModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, versionRepairEffective, installed.Version,
		"the repair must correct the recorded version, not leave it stale")

	gameCache := svc.GetGameCache(game)

	// The content must land under the CORRECT (effective) version's cache
	// slot - not get written into the stale recorded version's directory.
	assert.False(t, gameCache.Exists(game.ID, fixtureSourceID, versionRepairModID, versionRepairRecorded),
		"no content should ever be written into the stale recorded version's cache slot")
	require.True(t, gameCache.Exists(game.ID, fixtureSourceID, versionRepairModID, versionRepairEffective),
		"the redownload must land under the corrected (effective) version's cache slot")
	content, err := os.ReadFile(gameCache.GetFilePath(game.ID, fixtureSourceID, versionRepairModID, versionRepairEffective, versionRepairMember))
	require.NoError(t, err)
	assert.Equal(t, versionRepairPoisonText, string(content),
		"the effective version's slot must hold exactly what the source currently serves")

	// The job's own reported counts must be honest: a FRESH full-tier
	// verify run immediately afterward, on the same now-repaired state,
	// must agree nothing is left outstanding.
	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok, "the stored result must be the core document")
	assert.Zero(t, report.Result.Issues, "the job's own claimed issue count")
	assert.Zero(t, report.Result.Warnings, "the job's own claimed warning count")

	fresh, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)
	assert.Equal(t, report.Result.Issues, fresh.Result.Issues,
		"a fresh full-tier verify must agree with what the repair just claimed - not still see a version_mismatch")
	assert.Zero(t, fresh.Result.Issues, "the state really must be clean after the repair, not merely reported as clean")
}
