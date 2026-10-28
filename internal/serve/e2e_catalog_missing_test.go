package serve_test

// Issue 539 in the browser: an installed mod its source's catalog no longer
// has is named on the Updates card, with the way out, rather than turning
// the whole card into "Couldn't check for updates".

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goneCatalogSource is a fakeSource whose catalog has dropped every mod it
// is asked to check, answering the way Icarus answers a Firestore 404.
type goneCatalogSource struct{ *fakeSource }

func (s goneCatalogSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var errs []error
	for _, im := range installed {
		errs = append(errs, &source.ModNotFoundError{ModID: im.ID, Err: fmt.Errorf("%s: %w", im.ID, domain.ErrModNotFound)})
	}
	return nil, errors.Join(errs...)
}

func TestE2E_UpdatesCard_NamesAModItsCatalogNoLongerHas(t *testing.T) {
	f := newE2EFixture(t)
	f.Svc.RegisterSource(goneCatalogSource{newFakeSource("gonecat")})
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "dLs3nvmWj5uOPnXxezGe", SourceID: "gonecat", Name: "Bear Mount", Version: "1.0", GameID: f.Game.ID},
		ProfileName: f.Profile, UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, f.Svc.NewProfileManager().UpsertMod(t.Context(), f.Game.ID, f.Profile,
		domain.ModReference{SourceID: "gonecat", ModID: "dLs3nvmWj5uOPnXxezGe", Version: "1.0"}))

	var note string
	var cardError bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="updates-catalog-missing"]`, chromedp.ByQuery),
		chromedp.Text(`[data-testid="updates-catalog-missing"]`, &note, chromedp.ByQuery),
		chromedp.Evaluate(`Boolean(document.querySelector(".card--updates .card__error"))`, &cardError),
	)

	assert.Contains(t, note, "1 installed mod is no longer in its source's catalog: Bear Mount (gonecat)")
	assert.Contains(t, note, "Re-link")
	assert.Contains(t, note, "uninstall")
	assert.False(t, cardError, "a mod gone from its catalog is not a failed check")
	assert.Empty(t, f.BrowserErrors())
}
