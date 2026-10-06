package core_test

// #539: an installed mod whose source's catalog no longer has it (Project
// Daedalus recreated three Icarus mods under new IDs) failed the WHOLE
// source's update check. Core now reads the per-mod source.ModNotFoundError
// any source returns, reports those mods in UpdateCheckReport.CatalogMissing,
// and lets the rest of the check stand - generically, for every source.

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

// goneCatalogSource is a source whose catalog has dropped the ids in gone,
// fails the ids in broken, and offers newer for everything else.
type goneCatalogSource struct {
	*mockSource
	gone   map[string]bool
	broken map[string]bool
	newer  string
}

func (s *goneCatalogSource) Name() string { return "Gone Catalog" }

func (s *goneCatalogSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if s.gone[modID] {
		return nil, fmt.Errorf("fetching %s: HTTP 404: %w", modID, domain.ErrModNotFound)
	}
	return &domain.Mod{ID: modID, SourceID: s.id, Version: s.newer}, nil
}

func (s *goneCatalogSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	var errs []error
	for _, im := range installed {
		switch {
		case s.gone[im.ID]:
			errs = append(errs, &source.ModNotFoundError{ModID: im.ID, Err: fmt.Errorf("%s (id %s): %w", im.Name, im.ID, domain.ErrModNotFound)})
		case s.broken[im.ID]:
			errs = append(errs, fmt.Errorf("%s (id %s): HTTP 500", im.Name, im.ID))
		default:
			out = append(out, domain.Update{InstalledMod: im, NewVersion: s.newer})
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("source %q: %d update check(s) failed: %w", s.id, len(errs), errors.Join(errs...))
	}
	return out, nil
}

func newCatalogMissingService(t *testing.T, mods map[string]string, gone, broken []string) (*core.Service, *domain.Game, *goneCatalogSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	src := &goneCatalogSource{mockSource: newMockSource("gonecat"), gone: map[string]bool{}, broken: map[string]bool{}, newer: "2.0"}
	for _, id := range gone {
		src.gone[id] = true
	}
	for _, id := range broken {
		src.broken[id] = true
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	for id, name := range mods {
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:         domain.Mod{ID: id, SourceID: "gonecat", Name: name, Version: "1.0", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
		}))
		seedProfileWithMod(t, svc, game.ID, "default", "gonecat", id, "1.0")
	}
	return svc, game, src
}

func TestCheckGameUpdateReport_CatalogMissingIsReportedNotAnError(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t,
		map[string]string{"live": "Live Mod", "gone1": "Old One", "gone2": "Old Two"},
		[]string{"gone1", "gone2"}, nil)

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.NoError(t, err, "mods gone from the catalog are reported, not a failed check")
	assert.Empty(t, report.ErrorMessage)

	require.Len(t, report.Updates, 1, "the rest of the source's check stands")
	assert.Equal(t, "live", report.Updates[0].InstalledMod.ID)

	assert.ElementsMatch(t, []core.CatalogModRef{
		{SourceID: "gonecat", ModID: "gone1", Name: "Old One"},
		{SourceID: "gonecat", ModID: "gone2", Name: "Old Two"},
	}, report.CatalogMissing)
}

func TestCheckGameUpdateReport_OtherErrorsStillSurface(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t,
		map[string]string{"live": "Live Mod", "gone": "Old One", "flaky": "Flaky Mod"},
		[]string{"gone"}, []string{"flaky"})

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.Error(t, err, "a genuine failure still fails the check")
	assert.Contains(t, err.Error(), "Flaky Mod")
	assert.NotContains(t, err.Error(), "Old One", "the missing mod is reported in CatalogMissing, not in the error")
	assert.False(t, errors.Is(err, domain.ErrModNotFound))
	assert.Equal(t, err.Error(), report.ErrorMessage)

	require.Len(t, report.Updates, 1)
	assert.Equal(t, []core.CatalogModRef{{SourceID: "gonecat", ModID: "gone", Name: "Old One"}}, report.CatalogMissing)
}

// The batch plan the web UI's "update selected" builds runs the same check:
// one vanished mod must not refuse every other update.
func TestPlanUpdateBatch_CatalogMissingDoesNotFailThePlan(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t,
		map[string]string{"live": "Live Mod", "gone": "Old One"},
		[]string{"gone"}, nil)

	plan, err := svc.PlanUpdateBatch(context.Background(), game, "default", nil)
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	assert.Equal(t, "live", plan.Updates[0].InstalledMod.ID)
}

// A single-mod update of a vanished mod is NOT "up to date": it is refused
// naming the catalog, and stays ErrModNotFound to a caller branching on it.
func TestPlanUpdate_CatalogMissingModIsRefusedNamingTheCatalog(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t, map[string]string{"gone": "Old One"}, []string{"gone"}, nil)

	_, err := svc.PlanUpdate(context.Background(), game, "default", "gonecat", "gone")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	var missing *core.ModNotInCatalogError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, "gonecat", missing.SourceID)
	assert.Equal(t, "gone", missing.ModID)
	assert.Contains(t, err.Error(), "Gone Catalog's catalog")
}

// GetMod's not-found reads as a fact about the catalog, not the source's
// raw transport error (the full mod page's versions table, `lmm mod show`).
func TestGetMod_NotFoundNamesTheCatalog(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t, nil, []string{"gone"}, nil)

	_, err := svc.GetMod(context.Background(), "gonecat", game.ID, "gone")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	var missing *core.ModNotInCatalogError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, `mod gone is not in Gone Catalog's catalog (removed, or republished under a new ID)`, err.Error())
}

// `lmm mod show` / the full mod page's detail read say the same thing once.
func TestModDetail_NotInCatalogIsReadable(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t, nil, []string{"gone"}, nil)

	_, err := svc.ModDetail(context.Background(), game, "default", "gonecat", "gone")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Equal(t, `mod gone is not in Gone Catalog's catalog (removed, or republished under a new ID)`, err.Error())
}
