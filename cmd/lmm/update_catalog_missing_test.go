package main

// #539: `lmm update` on a game where an installed mod has left its source's
// catalog. The rest of the check stands, the run does not fail, and the
// vanished mod is named with the way out - relink it if it moved, or
// uninstall it - in text, and under --json's catalog_missing.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalogGoneSource is fakeUpdateSource whose catalog has dropped the ids
// in gone: its check reports each as a source.ModNotFoundError, the way
// Icarus answers a Firestore 404.
type catalogGoneSource struct {
	*fakeUpdateSource
	gone map[string]bool
}

func (s *catalogGoneSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var keep []domain.InstalledMod
	var errs []error
	for _, im := range installed {
		if s.gone[im.ID] {
			errs = append(errs, &source.ModNotFoundError{ModID: im.ID, Err: fmt.Errorf("%s (id %s): %w", im.Name, im.ID, domain.ErrModNotFound)})
			continue
		}
		keep = append(keep, im)
	}
	updates, err := s.fakeUpdateSource.CheckUpdates(ctx, keep)
	return updates, errors.Join(append(errs, err)...)
}

func (s *catalogGoneSource) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, _ source.UpdateProgressFunc) ([]domain.Update, error) {
	return s.CheckUpdates(ctx, installed)
}

// setupCatalogMissingTest installs "live" (with no newer version) and
// "dLs3nvmWj5uOPnXxezGe", which the catalog no longer has.
func setupCatalogMissingTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game, src := setupDoUpdateTest(t)
	ctx := context.Background()
	_, err := svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	for id, name := range map[string]string{"live": "Live Mod", "dLs3nvmWj5uOPnXxezGe": "Bear Mount"} {
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: id, SourceID: "test-src", Name: name, Version: "1.0", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
		}))
		require.NoError(t, svc.NewProfileManager().UpsertMod(ctx, game.ID, "default",
			domain.ModReference{SourceID: "test-src", ModID: id, Version: "1.0"}))
	}
	src.AddMod(&domain.Mod{ID: "live", SourceID: "test-src", Name: "Live Mod", Version: "1.0", GameID: game.ID}, nil)
	svc.RegisterSource(&catalogGoneSource{fakeUpdateSource: src, gone: map[string]bool{"dLs3nvmWj5uOPnXxezGe": true}})
	return svc, game
}

func TestDoUpdate_CatalogMissingIsNamedWithTheRemedy(t *testing.T) {
	svc, game := setupCatalogMissingTest(t)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err, "a mod gone from its catalog does not fail the run")
	assert.NotContains(t, out, "Warning:")
	assert.Contains(t, out, "All mods are up to date.")
	assert.Contains(t, out, "1 installed mod(s) are no longer in their source's catalog")
	assert.Contains(t, out, "Bear Mount (dLs3nvmWj5uOPnXxezGe, Fake Update Source)")
	assert.Contains(t, out, "lmm mod edit dLs3nvmWj5uOPnXxezGe -s test-src --to-source-id <new-id>")
	assert.Contains(t, out, "lmm uninstall dLs3nvmWj5uOPnXxezGe -s test-src")
}

func TestJSONGolden_UpdateBulkCatalogMissing(t *testing.T) {
	withJSONOutput(t)
	svc, game := setupCatalogMissingTest(t)

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_catalog_missing", out)
}

// #590: a gone mod with one likely successor (same name and author) names
// it with the ready-to-run re-link; several list every candidate and pick
// none; none keeps the #539 placeholder.
func TestPrintCatalogMissing_SuggestsTheSuccessor(t *testing.T) {
	svc, _ := setupCatalogMissingTest(t)
	gone := core.CatalogModRef{SourceID: "test-src", ModID: "old", Name: "Bear Mount"}

	one := gone
	one.Successors = []core.CatalogSuccessor{{ModID: "new", Name: "Bear Mount", Author: "Donovan", Version: "1.1"}}
	out := captureStdout(t, func() error {
		printCatalogMissing(svc, &core.UpdateCheckReport{CatalogMissing: []core.CatalogModRef{one}})
		return nil
	})
	assert.Contains(t, out, "    moved?   looks like new (same name and author): lmm mod edit old -s test-src --to-source-id new\n")
	assert.NotContains(t, out, "<new-id>")
	assert.Contains(t, out, "lmm uninstall old -s test-src")

	two := gone
	two.Successors = []core.CatalogSuccessor{{ModID: "new-a", Version: "1.0"}, {ModID: "new-b", Version: "1.2"}}
	out = captureStdout(t, func() error {
		printCatalogMissing(svc, &core.UpdateCheckReport{CatalogMissing: []core.CatalogModRef{two}})
		return nil
	})
	assert.Contains(t, out, "    moved?   several mods have the same name and author: new-a (1.0), new-b (1.2)\n")
	assert.Contains(t, out, "             lmm mod edit old -s test-src --to-source-id <one of them>\n")

	out = captureStdout(t, func() error {
		printCatalogMissing(svc, &core.UpdateCheckReport{CatalogMissing: []core.CatalogModRef{gone}})
		return nil
	})
	assert.Contains(t, out, "    moved:   lmm mod edit old -s test-src --to-source-id <new-id>\n")
}

// #590: Health's catalog_missing row says what #539 says, with the same
// suggestion, instead of "could not check version (source unreachable)".
func TestRenderVerifyFinding_CatalogMissingSuggestsTheSuccessor(t *testing.T) {
	out := captureStdout(t, func() error {
		renderVerifyFinding(core.VerifyEvent{Finding: core.VerifyFinding{
			Status: core.VerifyStatusCatalogMissing, ModID: "old", ModName: "Bear Mount", SourceID: "icarus",
			Note:       "no longer in Icarus's catalog (removed, or republished under a new ID)",
			Successors: []core.CatalogSuccessor{{ModID: "new"}},
		}})
		return nil
	})
	assert.Equal(t, "? Bear Mount - no longer in Icarus's catalog (removed, or republished under a new ID)\n"+
		"  moved?   looks like new (same name and author): lmm mod edit old -s icarus --to-source-id new\n"+
		"  removed: lmm uninstall old -s icarus\n", out)
}

// unstableGoneSource is catalogGoneSource declaring unstable IDs (#590),
// whose catalog a name search reads.
type unstableGoneSource struct {
	*catalogGoneSource
	catalog []domain.Mod
}

func (s *unstableGoneSource) UnstableIDs() bool { return true }

func (s *unstableGoneSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{Mods: s.catalog}, nil
}

// setupAutoRelinkTest is setupCatalogMissingTest over a source with
// unstable IDs, whose catalog has Bear Mount (by Donovan) under a new ID,
// and the gone mod's files in the cache.
func setupAutoRelinkTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupCatalogMissingTest(t)
	ctx := context.Background()
	src, err := svc.GetSource("test-src")
	require.NoError(t, err)
	gone := src.(*catalogGoneSource)
	im, err := svc.GetInstalledMod(ctx, "test-src", "dLs3nvmWj5uOPnXxezGe", game.ID, "default")
	require.NoError(t, err)
	im.Author = "Donovan"
	require.NoError(t, svc.SaveInstalledMod(ctx, im))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "dLs3nvmWj5uOPnXxezGe", "1.0", "bear.pak", []byte("pak")))
	moved := domain.Mod{ID: "8m1z04vNo2CCdKcD2C0H", SourceID: "test-src", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: game.ID}
	gone.AddMod(&moved, nil)
	svc.RegisterSource(&unstableGoneSource{catalogGoneSource: gone, catalog: []domain.Mod{moved}})
	return svc, game
}

// #590: `lmm update` re-links a gone mod of a source with unstable IDs to
// its one successor before anything is applied, and says so; the mod is
// then checked under its new ID.
func TestDoUpdate_ReLinksAGoneModOfAnUnstableSource(t *testing.T) {
	svc, game := setupAutoRelinkTest(t)

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "Bear Mount moved to a new ID in Fake Update Source; re-linked (dLs3nvmWj5uOPnXxezGe -> 8m1z04vNo2CCdKcD2C0H)")
	assert.NotContains(t, out, "no longer in their source's catalog", "the re-checked mod is not gone")
	assert.Contains(t, out, "All mods are up to date.")
	_, err = svc.GetInstalledMod(context.Background(), "test-src", "8m1z04vNo2CCdKcD2C0H", game.ID, "default")
	require.NoError(t, err)
}

// A dry run changes nothing: the suggestion is printed instead.
func TestDoUpdate_DryRunDoesNotReLink(t *testing.T) {
	svc, game := setupAutoRelinkTest(t)
	updateDryRun = true

	out, err := captureStdoutErr(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.NotContains(t, out, "re-linked")
	assert.Contains(t, out, "lmm mod edit dLs3nvmWj5uOPnXxezGe -s test-src --to-source-id 8m1z04vNo2CCdKcD2C0H")
	_, err = svc.GetInstalledMod(context.Background(), "test-src", "dLs3nvmWj5uOPnXxezGe", game.ID, "default")
	require.NoError(t, err, "still under its old id")
}

func TestJSONGolden_UpdateBulkAutoRelink(t *testing.T) {
	withJSONOutput(t)
	svc, game := setupAutoRelinkTest(t)
	updateAll = true

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_auto_relink", out)
}
