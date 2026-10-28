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
