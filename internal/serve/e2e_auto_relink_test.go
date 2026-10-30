package serve_test

// Issue 590's automatic half in the browser: a mod gone from the catalog of
// a source whose IDs can change, with exactly one same-name, same-author
// mod in it, is re-linked by the page's own update check - nobody clicks -
// and the Updates card and the activity tray say so.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unstableGoneSource is a fakeSource declaring unstable IDs, whose catalog
// no longer has the one id in gone.
type unstableGoneSource struct {
	*fakeSource
	gone string
}

func (unstableGoneSource) UnstableIDs() bool { return true }

func (s unstableGoneSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, im := range installed {
		if im.ID == s.gone {
			return nil, &source.ModNotFoundError{ModID: im.ID, Err: domain.ErrModNotFound}
		}
	}
	return nil, nil
}

func TestE2E_UpdatesCard_ReLinksAMovedModOnItsOwn(t *testing.T) {
	f := newE2EFixture(t)
	const oldID, newID = "dLs3nvmWj5uOPnXxezGe", "8m1z04vNo2CCdKcD2C0H"
	src := newFakeSource("gonecat")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: newID, SourceID: "gonecat", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: f.Game.ID},
		Files: []domain.DownloadableFile{{ID: "f1", FileName: "bear.zip", Version: "1.0", IsPrimary: true}},
	})
	f.Svc.RegisterSource(unstableGoneSource{fakeSource: src, gone: oldID})
	f.Game.SourceIDs["gonecat"] = ""
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: oldID, SourceID: "gonecat", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: f.Game.ID},
		ProfileName: f.Profile, UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"f1"},
	}))
	require.NoError(t, f.Svc.NewProfileManager().UpsertMod(t.Context(), f.Game.ID, f.Profile,
		domain.ModReference{SourceID: "gonecat", ModID: oldID, Version: "1.0"}))

	const relinked = `.card--updates [data-testid="updates-auto-relinked"]`
	var note, tray, notes string
	var buttons int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(relinked, chromedp.ByQuery),
		chromedp.Text(relinked, &note, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="relink-successor"]').length`, &buttons),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__kind[data-origin="auto"]`, chromedp.ByQuery),
		chromedp.Text(`.tray__kind[data-origin="auto"]`, &tray, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__notes`, chromedp.ByQuery),
		chromedp.Text(`.tray__notes`, &notes, chromedp.ByQuery),
	)

	assert.Contains(t, note, "Bear Mount moved to a new ID in")
	assert.Contains(t, note, "re-linked ("+oldID+" -> "+newID+")")
	assert.Equal(t, 0, buttons, "nothing is left to re-link by hand")
	assert.Equal(t, "catalog_relink (automatic)", tray)
	assert.Contains(t, notes, "re-linked ("+oldID+" -> "+newID+")")
	relinkedTo(t, f, newID)
	assert.Empty(t, f.BrowserErrors())
}
