package serve_test

// #657: the web UI's half of a NexusMods collection pinning a file
// NexusMods no longer offers. The plan view lists it as needing attention
// with the mod's current main file; Confirm waits until it is decided;
// choosing the current file re-plans, and the import then installs it.

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// e2eGoneTenSource is the Premium double whose list files Ten's pinned
// file as archived.
type e2eGoneTenSource struct{ e2ePremiumNexusSource }

func (e2eGoneTenSource) ResolveGameCollection(_ context.Context, gameSourceID, ref string) (source.Collection, error) {
	c := e2eNexusCollection(gameSourceID, ref)
	c.Items[0].FileCategory = "ARCHIVED"
	return c, nil
}

func TestE2E_NexusCollection_ChooseTheCurrentFileThenImport(t *testing.T) {
	sandboxE2EEnv(t)
	src := newE2ESearchSource(t, "nexusmods")
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "10", SourceID: "nexusmods", Name: "Ten", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "100", Name: "Old", FileName: "ten-1.zip", Version: "1.0", Category: "ARCHIVED", Size: 16},
			{ID: "101", Name: "Ten Two", FileName: "ten-2.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 16},
		},
		members: map[string]string{"101": "ten.esp"},
	})
	src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "11", SourceID: "nexusmods", Name: "Eleven", Version: "1.1"},
		files:   []domain.DownloadableFile{{ID: "110", Name: "Main", FileName: "eleven.zip", Version: "1.1", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"110": "eleven.esp"},
	})
	f := newE2EFixture(t)
	f.Svc.RegisterSource(e2eGoneTenSource{e2ePremiumNexusSource{src}})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "abcdef", e2eNexusRecipe)

	var attention, decided string
	var confirmDisabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("abcdef"),
		chromedp.WaitVisible(`[data-testid="gone-pins"]`, chromedp.ByQuery),
		textContent(`[data-testid="gone-pins"]`, &attention),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &confirmDisabled),
		clickWhenSettled(`[data-gone-pin="nexusmods:10"] [data-testid="gone-pin-use-current"]`),
		pollUntil(`document.querySelector('[data-gone-pin="nexusmods:10"]')?.dataset.choice === "use_current"`),
		textContent(`[data-testid="gone-pins"]`, &decided),
		pollUntil(`!document.querySelector('.modal [data-action="confirm"]').disabled`),
		chromedp.Click(`.modal .form-check input[type="checkbox"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, attention, "Needs your attention - 1 mod pins a file NexusMods no longer offers")
	assert.Contains(t, attention, "The collection pins file 100, version 1.0")
	assert.Contains(t, attention, "Install the current file instead (suggested): Ten Two (version 2.0)")
	assert.Contains(t, attention, "Skip this mod")
	assert.True(t, confirmDisabled, "Confirm waits until every gone pin is decided")
	assert.NotEmpty(t, decided)

	require.Eventually(t, func() bool {
		rows, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, "Three Mods")
		return err == nil && len(rows) == 2
	}, 15*time.Second, 50*time.Millisecond, "both items downloaded and installed")
	row, err := f.Svc.GetInstalledMod(t.Context(), "nexusmods", "10", f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version, "the current file was installed")
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, &domain.CollectionPin{Version: "1.0", FileIDs: []string{"100"}}, p.FindRef("nexusmods", "10").SubstitutedFor)
	assert.Empty(t, f.BrowserErrors())
}
