package serve

// C1 (epic live review, .superpowers/sdd/2026-08-30-serve-impl/epic-live-
// review.md): the repair ran at core.VerifyLocal, so its version pass
// (the network-touching phase that detects a version_mismatch and corrects
// the recorded version FIRST) never ran. perFileWalk's "missing" repair then
// re-downloaded the file the source CURRENTLY reports and stored it under
// the STILL-recorded (stale) version's cache directory - silently writing a
// newer version's content into an older version's slot while the DB kept
// claiming the old version was intact. `lmm verify --fix` never has this bug
// because it always runs at core.VerifyFull, so the version record is
// corrected before perFileWalk ever looks at the cache.
//
// Ported to the /api/v1 Plan -> job entry point with the deletion of the
// server-rendered page layer (docs/plans/2026-08-31-serve-spa-design.md);
// the fixture and every state assertion are unchanged.
//
// This fixture reproduces the review's exact repro: a mod recorded at
// "2.0.0" with no cache dir for that version at all (simulating "the cache
// dir was deleted"), while the source's current listing for the same file ID
// reports "3.0.0" with different content ("sash v3 POISON", straight out of
// the review's own transcript).

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestFlowHealthFix_RepairsVersionMismatch_NotJustTheMissingFile is C1's
// headline RED test. Before the fix (kind_verify_fix.go pinned to
// core.VerifyLocal), the repair "succeeds" by writing the source's CURRENT
// (3.0.0) content into the cache slot for the STALE recorded (2.0.0)
// version, leaving the DB claiming 2.0.0 is intact and a clean bill of
// health - exactly the review's reproduction. After the fix
// (core.VerifyFull, matching the CLI and /api/v1/health), the version
// record is corrected FIRST, so the redownload lands in the correct 3.0.0
// slot with a matching DB record, and a fresh full-tier verify immediately
// afterward agrees nothing is left to fix.
func TestFlowHealthFix_RepairsVersionMismatch_NotJustTheMissingFile(t *testing.T) {
	s, svc, game := newVersionMismatchFixtureServer(t)

	j := runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	ctx := t.Context()

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

// TestFlowVerifyFixPlan_TierMatchesTheAPIAndTheCLI is the leg
// api_health_test.go's C1 three-way count check lost when the /health page
// went away: the repair PLAN (the dry run a client previews) must be run at
// the same tier GET /api/v1/health and the CLI's own VerifyFull call use.
// A plan/apply tier mismatch here is precisely the corruption
// kind_verify_fix.go's doc comment warns can resurrect, so the preview
// agreeing with the other two surfaces is the property worth pinning.
func TestFlowVerifyFixPlan_TierMatchesTheAPIAndTheCLI(t *testing.T) {
	s, svc, game := newVersionMismatchFixtureServer(t)

	_, raw := planFlow(t, s, game, "verify_fix", "")
	var planned struct {
		Plan core.VerifyReport `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &planned))
	require.NotNil(t, planned.Plan.Result)

	apiRec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game), "")
	require.Equal(t, http.StatusOK, apiRec.Code)
	var apiReport core.VerifyReport
	require.NoError(t, json.Unmarshal(apiRec.Body.Bytes(), &apiReport))

	cliEquivalent, err := svc.VerifyReport(t.Context(), game, "default", core.VerifyOptions{Tier: core.VerifyFull}, nil)
	require.NoError(t, err)

	require.Positive(t, planned.Plan.Result.Issues,
		"the repair preview must see the version_mismatch, not report a clean sheet")
	assert.Equal(t, planned.Plan.Result.Issues, apiReport.Result.Issues,
		"the repair preview and /api/v1/health must agree on the same state")
	assert.Equal(t, apiReport.Result.Issues, cliEquivalent.Result.Issues,
		"and both must agree with the CLI's own tier")
}

// TestFlowHealthFix_LockedFinding_StaysOutstanding is N-3's own test (epic
// re-review): a locked mod's version_mismatch cannot be repaired
// (internal/core/verify.go's lock refusal), so it comes back from the SAME
// repair run still outstanding rather than resolved. C1's tier change made
// this the COMMON outcome, not a corner case: a version mismatch is visible
// at all now, and a locked one can never be repaired.
//
// The original test asserted this through the ?sync=1 result page's amber
// "Done, with failures" banner - the display rule M6/N-3 were about. That
// banner went with the page layer; the fact it was reading is on the
// result document, and that is what this pins: the job succeeds, the
// finding is reported as refused because of the lock, and the outstanding
// count is NOT zeroed out by the repair that could not run.
func TestFlowHealthFix_LockedFinding_StaysOutstanding(t *testing.T) {
	s, svc, game := newVersionMismatchFixtureServer(t)
	_, err := svc.SetModLock(t.Context(), fixtureSourceID, versionRepairModID, game.ID, "default", "")
	require.NoError(t, err)

	j := runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok, "the stored result must be the core document")
	assert.Equal(t, 1, report.Result.Issues,
		"the locked mismatch must still be counted as outstanding, not zeroed out by the refused repair")

	var refused *core.VerifyFinding
	for i := range report.Result.Findings {
		if strings.Contains(report.Result.Findings[i].Note, "locked") {
			refused = &report.Result.Findings[i]
		}
	}
	require.NotNil(t, refused, "the refusal must name the lock as its reason, not be silently dropped")
	assert.NotEqual(t, "fixed_version_mismatch", refused.Status, "a refused repair is not a repair")
}
