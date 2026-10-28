package serve

// Issue 539 through the endpoints the SPA reads: GET /api/v1/updates names
// an installed mod its source's catalog no longer has in catalog_missing -
// not as an update, and not as a failed check - and the full mod page's
// versions read answers such a mod with a 404 whose message names the
// catalog rather than the source's raw transport error.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goneCatalogFixtureSource is a source whose catalog has dropped every mod.
type goneCatalogFixtureSource struct{ fixtureSource }

func (*goneCatalogFixtureSource) ID() string   { return "gonecat" }
func (*goneCatalogFixtureSource) Name() string { return "Gone Catalog" }

func (*goneCatalogFixtureSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var errs []error
	for _, im := range installed {
		errs = append(errs, &source.ModNotFoundError{ModID: im.ID, Err: fmt.Errorf("%s: %w", im.ID, domain.ErrModNotFound)})
	}
	return nil, fmt.Errorf("source %q: %d update check(s) failed: %w", "gonecat", len(errs), errors.Join(errs...))
}

func TestAPIFlow_Updates_NamesAModItsCatalogNoLongerHas(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	svc.RegisterSource(&goneCatalogFixtureSource{})
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "dLs3nvmWj5uOPnXxezGe", SourceID: "gonecat", Name: "Bear Mount", Version: "1.0", GameID: game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.Empty(t, report.ErrorMessage, "a mod gone from its catalog is not a failed check")
	assert.Empty(t, report.Updates)
	assert.Equal(t, []core.CatalogModRef{{SourceID: "gonecat", ModID: "dLs3nvmWj5uOPnXxezGe", Name: "Bear Mount"}}, report.CatalogMissing)
}

func TestAPIFlow_ModVersions_NotInCatalogIsAReadable404(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/mods/"+fixtureSourceID+"/dLs3nvmWj5uOPnXxezGe/versions", game), "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	var envelope apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "mod dLs3nvmWj5uOPnXxezGe is not in Fixture Source's catalog (removed, or republished under a new ID)", envelope.Error)
}
