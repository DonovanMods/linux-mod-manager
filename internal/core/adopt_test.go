package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for ScanLocal / PlanAdopt / ApplyAdoptBackfill / ApplyAdopt - the
// core twin of the CLI's `lmm import` scan mode (v2 Phase 2 Unit K Task 18,
// #291). cmd/lmm's TestRunImportScan_* characterization tests keep pinning
// the printed lines; these pin the classification, the plan contents, the
// end state, the event stream and the plan-staleness guard.

// adoptTestSource is a searchable source double: Search returns searchMods
// verbatim (or searchErr), GetMod answers from mods, GetModFiles returns
// files (or filesErr). Named apart from the package's mockSource because
// these tests need per-call error injection and an explicit file list.
type adoptTestSource struct {
	id         string
	caps       source.Capabilities
	searchMods []domain.Mod
	searchErr  error
	mods       map[string]*domain.Mod
	modErr     error
	files      []domain.DownloadableFile
	filesErr   error
}

func newAdoptTestSource(id string) *adoptTestSource {
	return &adoptTestSource{
		id:   id,
		caps: source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true},
		mods: make(map[string]*domain.Mod),
	}
}

func (s *adoptTestSource) ID() string                        { return s.id }
func (s *adoptTestSource) Name() string                      { return s.id }
func (s *adoptTestSource) AuthURL() string                   { return "" }
func (s *adoptTestSource) Capabilities() source.Capabilities { return s.caps }
func (s *adoptTestSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}

func (s *adoptTestSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	if s.searchErr != nil {
		return source.SearchResult{}, s.searchErr
	}
	return source.SearchResult{Mods: s.searchMods, TotalCount: len(s.searchMods)}, nil
}

func (s *adoptTestSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if s.modErr != nil {
		return nil, s.modErr
	}
	if mod, ok := s.mods[modID]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}

func (s *adoptTestSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *adoptTestSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.filesErr != nil {
		return nil, s.filesErr
	}
	return s.files, nil
}

func (s *adoptTestSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", nil
}

func (s *adoptTestSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// newAdoptTestService builds a service plus a copy-mode game registered with
// it (SourcesForGame resolves the game through the service's own registry)
// and an empty "default" profile.
func newAdoptTestService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, DeployMode: domain.DeployCopy,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), game.ID, "default"))
	return svc, game
}

// writeLooseMod drops an untracked mod archive into the game's mod_path.
func writeLooseMod(t *testing.T, game *domain.Game, name, content string) string {
	t.Helper()
	path := filepath.Join(game.ModPath, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// stepDetails collects every StepEvent detail from a recorded stream, in
// order, so a test can pin the exact sequence a frontend would render.
func stepDetails(events []core.Event) []string {
	var out []string
	for _, e := range events {
		if se, ok := e.(core.StepEvent); ok {
			out = append(out, se.Detail)
		}
	}
	return out
}

// --- ScanLocal ---

// TestScanLocal_ExtractModeWarningOnEmptyScan pins the two facts the CLI's
// leading caveat note and "Found 0 files, 0 untracked" line are rendered
// from, for a game that is not in copy mode.
func TestScanLocal_ExtractModeWarningOnEmptyScan(t *testing.T) {
	svc, game := newAdoptTestService(t)
	game.DeployMode = domain.DeployExtract

	scan, err := svc.ScanLocal(context.Background(), game, core.ScanOptions{ProfileName: "default"})
	require.NoError(t, err)

	assert.True(t, scan.ExtractModeWarning)
	assert.Empty(t, scan.Tracked)
	assert.Empty(t, scan.Untracked)
	assert.Empty(t, scan.Backfill)
}

// TestScanLocal_SplitsTrackedAndUntracked pins the split the CLI's
// "Found %d files, %d untracked" line counts: Tracked+Untracked is every
// scanned entry, Untracked is what an adopt would touch.
func TestScanLocal_SplitsTrackedAndUntracked(t *testing.T) {
	svc, game := newAdoptTestService(t)
	writeLooseMod(t, game, "LooseMod-1.0.zip", "loose")
	writeLooseMod(t, game, "CoolMod-2.0.zip", "cool")
	// isFileTracked matches the installed NAME against the file's basename,
	// so the tracked fixture is named for the file it owns.
	seedSyncInstalledMod(t, svc, game, domain.SourceLocal, "existing-id", "CoolMod-2.0", "1.0", "default", true, nil)

	scan, err := svc.ScanLocal(context.Background(), game, core.ScanOptions{ProfileName: "default"})
	require.NoError(t, err)

	assert.False(t, scan.ExtractModeWarning, "a copy-mode game gets no extract caveat")
	require.Len(t, scan.Tracked, 1)
	assert.Equal(t, "CoolMod-2.0.zip", scan.Tracked[0].FileName)
	require.Len(t, scan.Untracked, 1)
	assert.Equal(t, "LooseMod-1.0.zip", scan.Untracked[0].FileName)
	assert.Equal(t, domain.SourceLocal, scan.Untracked[0].MatchedSource)
}

// TestScanLocal_BackfillCandidates_SourceLinkedRowsMissingMetadata pins the
// candidate set the CLI's "Backfilling metadata for %d mod(s)..." line
// counts: a source-linked row missing Author or SourceURL. A local mod, and
// a row that already has both, are not candidates.
func TestScanLocal_BackfillCandidates_SourceLinkedRowsMissingMetadata(t *testing.T) {
	svc, game := newAdoptTestService(t)
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, nil)
	seedSyncInstalledMod(t, svc, game, domain.SourceLocal, "loc", "Local Mod", "1.0", "default", true, nil)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "88", SourceID: "acme-source", Name: "Already Complete", Version: "1.0", GameID: game.ID,
			Author: "Someone", SourceURL: "http://example.com/88",
		},
		ProfileName: "default", Enabled: true, UpdatePolicy: domain.UpdateNotify,
	}))

	scan, err := svc.ScanLocal(context.Background(), game, core.ScanOptions{ProfileName: "default"})
	require.NoError(t, err)

	require.Len(t, scan.Backfill, 1)
	assert.Equal(t, "77", scan.Backfill[0].ID)
}

// --- PlanAdopt ---

// TestPlanAdopt_SkipMatch_DropsBackfillAndRunsNoLookups pins --skip-match's
// two effects at once: the backfill candidates are dropped (the CLI's whole
// backfill block never runs) and no source lookup is attempted, so the
// untracked entry keeps ScanModPath's "local" default.
func TestPlanAdopt_SkipMatch_DropsBackfillAndRunsNoLookups(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "LooseMod", GameID: "g1"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, nil)
	writeLooseMod(t, game, "LooseMod-1.0.zip", "loose")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)

	assert.True(t, plan.SkipMatch)
	assert.Empty(t, plan.Scan.Backfill, "--skip-match drops the backfill candidates entirely")
	require.Len(t, plan.Matches, 1)
	assert.Nil(t, plan.Matches[0].Mod, "--skip-match performs no source lookup")
	assert.Equal(t, domain.SourceLocal, plan.Matches[0].Untracked.MatchedSource)
}

// TestPlanAdopt_MatchUpgradesScanResultAndResolvesFile pins the whole
// matched path: the source hit is recorded on the match, the untracked
// ScanResult is upgraded in place (ID/SourceID/Name/MatchedSource), and the
// file whose FileName matches exactly is resolved onto it.
func TestPlanAdopt_MatchUpgradesScanResultAndResolvesFile(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", Author: "Jane", GameID: "g1"}}
	src.files = []domain.DownloadableFile{
		{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"},
		{ID: "78", FileName: "AcmeMod-2.0.zip", Version: "2.0"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	require.Len(t, plan.Matches, 1)
	m := plan.Matches[0]
	require.NotNil(t, m.Mod)
	assert.Equal(t, "42", m.Mod.ID)
	require.NotNil(t, m.File)
	assert.Equal(t, "77", m.File.ID)
	assert.Empty(t, m.Error)
	assert.Empty(t, m.FileError)

	require.NotNil(t, m.Untracked.Mod)
	assert.Equal(t, "42", m.Untracked.Mod.ID)
	assert.Equal(t, "acme-source", m.Untracked.Mod.SourceID)
	assert.Equal(t, "AcmeMod", m.Untracked.Mod.Name)
	assert.Equal(t, "Jane", m.Untracked.Mod.Author)
	assert.Equal(t, "acme-source", m.Untracked.MatchedSource)
	require.NotNil(t, m.Untracked.ResolvedFile)
	assert.Equal(t, "77", m.Untracked.ResolvedFile.ID)

	require.Len(t, plan.Scan.Untracked, 1)
	assert.Equal(t, "acme-source", plan.Scan.Untracked[0].MatchedSource,
		"the scan's own entry is upgraded too, so both views agree")
}

// TestPlanAdopt_NoMatch_StaysLocal pins the "○ ... -> local (no match)"
// path: a clean search with zero results leaves the entry local and records
// no error.
func TestPlanAdopt_NoMatch_StaysLocal(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source") // no searchMods: succeeds with zero results
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	writeLooseMod(t, game, "LooseMod-1.0.zip", "loose")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	require.Len(t, plan.Matches, 1)
	assert.Nil(t, plan.Matches[0].Mod)
	assert.Empty(t, plan.Matches[0].Error)
	assert.Equal(t, domain.SourceLocal, plan.Matches[0].Untracked.MatchedSource)
}

// TestPlanAdopt_AllSourcesError_RecordsErrorNotFailure pins tryMatchSources'
// error semantics at the plan level: every searchable source failing is a
// per-entry Error string, never a failed PlanAdopt.
func TestPlanAdopt_AllSourcesError_RecordsErrorNotFailure(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchErr = errors.New("boom")
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	writeLooseMod(t, game, "LooseMod-1.0.zip", "loose")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	require.Len(t, plan.Matches, 1)
	assert.Nil(t, plan.Matches[0].Mod)
	assert.Contains(t, plan.Matches[0].Error, "boom")
}

// TestPlanAdopt_FileResolutionFailure_RecordsFileErrorAndKeepsMatch pins the
// non-fatal half of #139: a failed source-file listing leaves the match
// intact and marker-less, with the reason recorded for the CLI's
// --verbose-only "could not resolve source file" line.
func TestPlanAdopt_FileResolutionFailure_RecordsFileErrorAndKeepsMatch(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1"}}
	src.filesErr = errors.New("rate limited")
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	require.Len(t, plan.Matches, 1)
	require.NotNil(t, plan.Matches[0].Mod)
	assert.Nil(t, plan.Matches[0].File)
	assert.Contains(t, plan.Matches[0].FileError, "rate limited")
	assert.Empty(t, plan.Matches[0].Error, "a file-listing failure is not a match failure")
}

// TestPlanAdopt_Duplicates_ListsUntrackedThatDuplicateAnInstalledMod pins
// the plan-time duplicate preview FindDuplicateMod produces.
func TestPlanAdopt_Duplicates_ListsUntrackedThatDuplicateAnInstalledMod(t *testing.T) {
	svc, game := newAdoptTestService(t)
	seedSyncInstalledMod(t, svc, game, domain.SourceLocal, "existing-id", "CoolMod", "1.0", "default", true, nil)
	// A name the tracked check misses (the installed name is "CoolMod", the
	// file is "Cool-Mod-2.0.zip") but FindDuplicateMod's normalization catches.
	writeLooseMod(t, game, "Cool-Mod-2.0.zip", "dup")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)

	require.Len(t, plan.Matches, 1)
	assert.Equal(t, []string{"Cool-Mod-2.0.zip"}, plan.Duplicates)
}

// --- ApplyAdoptBackfill ---

// TestApplyAdoptBackfill_SavesFetchedMetadata pins the backfill's DB write
// and its --verbose-only success event.
func TestApplyAdoptBackfill_SavesFetchedMetadata(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["77"] = &domain.Mod{
		ID: "77", SourceID: "acme-source", Name: "Needs Backfill", Author: "Jane Modder",
		SourceURL: "http://example.com/mod77", Version: "1.0", GameID: "g1",
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, nil)

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyAdoptBackfill(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Backfilled)

	updated, gErr := svc.GetInstalledMod(context.Background(), "acme-source", "77", "g1", "default")
	require.NoError(t, gErr)
	assert.Equal(t, "Jane Modder", updated.Author)
	assert.Equal(t, "http://example.com/mod77", updated.SourceURL)

	assert.Equal(t, []string{"✓ Needs Backfill: metadata updated (author: Jane Modder)"}, stepDetails(*events))
}

// TestApplyAdoptBackfill_FetchFailure_NotesAndCountsNothing pins the
// non-fatal fetch failure: the row is untouched, the count stays 0, and the
// reason arrives as a --verbose-only note.
func TestApplyAdoptBackfill_FetchFailure_NotesAndCountsNothing(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.modErr = errors.New("upstream down")
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, nil)

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyAdoptBackfill(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Backfilled)

	require.Len(t, stepDetails(*events), 1)
	assert.Contains(t, stepDetails(*events)[0], "Needs Backfill: metadata fetch failed: ")
	assert.Contains(t, stepDetails(*events)[0], "upstream down")

	unchanged, gErr := svc.GetInstalledMod(context.Background(), "acme-source", "77", "g1", "default")
	require.NoError(t, gErr)
	assert.Empty(t, unchanged.Author)
}

// TestApplyAdoptBackfill_UnmappedSource_SilentlySkipped pins the silent
// skip: a row whose source has no game-ID mapping is not fetched, not
// saved, and produces no event at all.
func TestApplyAdoptBackfill_UnmappedSource_SilentlySkipped(t *testing.T) {
	svc, game := newAdoptTestService(t)
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, nil)

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Scan.Backfill, 1, "the candidate is still counted for the CLI's announce line")

	sink, events := core.RecordEvents()
	result, err := svc.ApplyAdoptBackfill(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Backfilled)
	assert.Empty(t, *events)
}

// --- ApplyAdopt ---

// TestApplyAdopt_CopyMode_WritesCacheSavesRowAndProfile pins the whole
// adoption of one unmatched local mod: the cache entry holds the source
// file's bytes, the DB row is manual-download, and the profile carries the
// ref.
func TestApplyAdopt_CopyMode_WritesCacheSavesRowAndProfile(t *testing.T) {
	svc, game := newAdoptTestService(t)
	writeLooseMod(t, game, "LooseMod-1.0.zip", "loose-payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyAdopt(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)
	assert.Equal(t, 0, result.Skipped)
	assert.Equal(t, 0, result.Failed)
	assert.Equal(t, []string{"✓ LooseMod"}, stepDetails(*events))

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1)
	installed := mods[0]
	assert.Equal(t, domain.SourceLocal, installed.SourceID)
	assert.True(t, installed.ManualDownload)
	assert.True(t, installed.Deployed)
	assert.True(t, installed.Enabled)
	assert.Empty(t, installed.FileIDs)

	cached := filepath.Join(svc.GlobalCacheDir(), "g1", "local-"+installed.ID, "1.0", "LooseMod-1.0.zip")
	data, rErr := os.ReadFile(cached)
	require.NoError(t, rErr, "copy-mode adoption must write the source file into the cache")
	assert.Equal(t, "loose-payload", string(data))

	profile, pErr := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, pErr)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, installed.ID, profile.Mods[0].ModID)
}

// TestApplyAdopt_MatchedFile_RecordsFileIDsAndStampsMarker is the #139
// twin: an adoption whose plan resolved a source file records the file ID on
// the row and stamps the completion marker on the cache entry it writes.
func TestApplyAdopt_MatchedFile_RecordsFileIDsAndStampsMarker(t *testing.T) {
	svc, game := newAdoptTestService(t)
	src := newAdoptTestSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1"}}
	src.files = []domain.DownloadableFile{
		{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"},
		{ID: "78", FileName: "AcmeMod-2.0.zip", Version: "2.0"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	writeLooseMod(t, game, "AcmeMod-1.0.zip", "payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)

	installed, gErr := svc.GetInstalledMod(context.Background(), "acme-source", "42", "g1", "default")
	require.NoError(t, gErr)
	assert.Equal(t, []string{"77"}, installed.FileIDs)
	assert.True(t, installed.ManualDownload,
		"adoption keeps its manual-download semantics regardless of file resolution")
	assert.True(t, svc.GetGameCache(game).HasFileIDs("g1", "acme-source", "42", installed.Version, []string{"77"}),
		"the adoption-written cache entry must carry the resolved file's completion marker")
}

// TestApplyAdopt_Duplicate_SkippedWithoutDBOrCacheWrite pins the duplicate
// skip: nothing is saved, nothing is cached, and the reason is reported at
// its point of occurrence.
func TestApplyAdopt_Duplicate_SkippedWithoutDBOrCacheWrite(t *testing.T) {
	svc, game := newAdoptTestService(t)
	seedSyncInstalledMod(t, svc, game, domain.SourceLocal, "existing-id", "CoolMod", "1.0", "default", true, nil)
	writeLooseMod(t, game, "Cool-Mod-2.0.zip", "dup-payload")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)

	sink, events := core.RecordEvents()
	result, err := svc.ApplyAdopt(context.Background(), game, plan, sink)
	require.NoError(t, err)
	assert.Equal(t, 0, result.Adopted)
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, []string{`⊘ Cool-Mod-2.0.zip: skipped (duplicate of "CoolMod")`}, stepDetails(*events))

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1, "only the pre-existing row remains")
	assert.Equal(t, "existing-id", mods[0].ID)

	cacheMatches, gErr := filepath.Glob(filepath.Join(svc.GlobalCacheDir(), "g1", "local-*"))
	require.NoError(t, gErr)
	assert.Empty(t, cacheMatches, "a skipped duplicate must not write a cache entry")
}

// TestApplyAdopt_ExtractMode_TracksInPlaceWithoutCaching pins the
// extract-mode caveat's actual behaviour: the row is saved, but no cache
// entry is written.
func TestApplyAdopt_ExtractMode_TracksInPlaceWithoutCaching(t *testing.T) {
	svc, game := newAdoptTestService(t)
	game.DeployMode = domain.DeployExtract
	require.NoError(t, os.MkdirAll(filepath.Join(game.ModPath, "LooseMod-1.0"), 0755))

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)

	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1)

	cacheMatches, gErr := filepath.Glob(filepath.Join(svc.GlobalCacheDir(), "g1", "local-*"))
	require.NoError(t, gErr)
	assert.Empty(t, cacheMatches, "extract-mode adoption tracks in place, without caching")
}

// TestApplyAdopt_DeployCompile_SyncsMergedPak pins #197's tail on the adopt
// path: registering a mod is a mod-set change, so the merged pak is synced
// after every adoption. The game's only mods are local (no merge sources),
// so the sync takes its uninstall-to-zero branch and clears the merged pak
// that was there - proving it ran at all.
func TestApplyAdopt_DeployCompile_SyncsMergedPak(t *testing.T) {
	svc, game := newAdoptTestService(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	require.NoError(t, svc.SaveGame(context.Background(), game))

	// "merged-pak"/"merged" mirror core's private mergedPakModID/
	// mergedPakVersion (same convention as merged_pak_hooks_test.go).
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, domain.SourceMerged, "merged-pak", "merged", "zzz_LMM_Merged_P.pak", []byte("merged-bytes")))

	// A non-copy game scans mod_path for DIRECTORIES.
	require.NoError(t, os.MkdirAll(filepath.Join(game.ModPath, "LooseMod-1.0"), 0755))

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)
	require.Len(t, plan.Matches, 1)

	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)

	assert.False(t, gameCache.Exists(game.ID, domain.SourceMerged, "merged-pak", "merged"),
		"adopting a mod must sync the merged pak")
}

// TestApplyAdopt_WithinBatchDuplicate_SkipsTheSecond pins the growing
// duplicate set: two files whose names normalize to the same mod adopt once,
// even though neither duplicates anything installed at plan time.
func TestApplyAdopt_WithinBatchDuplicate_SkipsTheSecond(t *testing.T) {
	svc, game := newAdoptTestService(t)
	writeLooseMod(t, game, "DupMod-1.0.zip", "one")
	writeLooseMod(t, game, "DupMod-2.0.zip", "two")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)
	assert.Empty(t, plan.Duplicates, "neither file duplicates anything installed at plan time")

	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Adopted)
	assert.Equal(t, 1, result.Skipped)
}

// TestApplyAdopt_StalePlan_Refused pins ruling 5: an adopt plan computed
// before the profile's installed-mod set changed is refused, with nothing
// adopted.
func TestApplyAdopt_StalePlan_Refused(t *testing.T) {
	svc, game := newAdoptTestService(t)
	writeLooseMod(t, game, "LooseMod-1.0.zip", "loose")

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{SkipMatch: true})
	require.NoError(t, err)

	seedSyncInstalledMod(t, svc, game, "src", "late", "Late Arrival", "1.0", "default", true, nil)

	result, err := svc.ApplyAdopt(context.Background(), game, plan, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
	assert.Equal(t, 0, result.Adopted)

	mods, mErr := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, mErr)
	require.Len(t, mods, 1, "only the mod that made the plan stale is installed")
	assert.Equal(t, "late", mods[0].ID)
}

// TestApplyAdoptBackfill_StalePlan_Refused is the same guard on the
// backfill half - both applies re-derive the snapshot the plan recorded.
func TestApplyAdoptBackfill_StalePlan_Refused(t *testing.T) {
	svc, game := newAdoptTestService(t)
	seedSyncInstalledMod(t, svc, game, "acme-source", "77", "Needs Backfill", "1.0", "default", true, nil)

	plan, err := svc.PlanAdopt(context.Background(), game, "default", core.AdoptOptions{})
	require.NoError(t, err)

	seedSyncInstalledMod(t, svc, game, "src", "late", "Late Arrival", "1.0", "default", true, nil)

	result, err := svc.ApplyAdoptBackfill(context.Background(), game, plan, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
	assert.Equal(t, 0, result.Backfilled)
}
