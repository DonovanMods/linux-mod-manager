package serve_test

// #657: the web UI's half of a NexusMods collection pinning a file
// NexusMods no longer offers. The plan view lists it as needing attention
// with the mod's current main file, preselected as the default (#660), and
// the import installs it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// e2eGoneTenSource is the Premium double whose list has no record of
// Ten's pinned file: its author removed it.
type e2eGoneTenSource struct{ e2ePremiumNexusSource }

func (e2eGoneTenSource) ResolveGameCollection(_ context.Context, gameSourceID, ref string) (source.Collection, error) {
	c := e2eNexusCollection(gameSourceID, ref)
	c.Items[0].ModID = ""
	return c, nil
}

func TestE2E_NexusCollection_TheCurrentFileIsTheDefault(t *testing.T) {
	sandboxE2EEnv(t)
	src := newE2ESearchSource(t, "nexusmods")
	src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "10", SourceID: "nexusmods", Name: "Ten", Version: "2.0"},
		files: []domain.DownloadableFile{
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

	// #660 owner ruling: the current file is preselected, with Skip beside
	// it, and Confirm is enabled at once.
	var attention, line string
	var confirmDisabled bool
	var choice string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("abcdef"),
		chromedp.WaitVisible(`[data-testid="gone-pins"]`, chromedp.ByQuery),
		textContent(`[data-testid="gone-pins"]`, &attention),
		textContent(`[data-gone-pin="nexusmods:10"] [data-testid="gone-pin-default"]`, &line),
		chromedp.Evaluate(`document.querySelector('[data-gone-pin="nexusmods:10"]').dataset.choice`, &choice),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &confirmDisabled),
		chromedp.Click(`.modal .form-check input[type="checkbox"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, attention, "Needs your attention - 1 mod pins a file NexusMods no longer offers")
	assert.Contains(t, attention, "The collection pins file 100, version 1.0")
	assert.Contains(t, attention, "File 100 (its main file): current main file Ten Two (version 2.0)")
	assert.Contains(t, attention, "Install the current file instead (the default)")
	assert.Contains(t, attention, "Skip this mod")
	assert.Equal(t, "use_current", choice, "the current file is preselected")
	assert.Contains(t, line, "The curator pinned 1.0 exactly, and NexusMods no longer offers it; installing the current 2.0 instead.")
	assert.False(t, confirmDisabled, "nothing to decide first")

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

// e2eGoneNexusSource is the free-account NexusMods double whose list has
// no record of Ten's pinned file 100 (its author removed it); 101 is Ten's
// main file now. rev4 serves revision 4, which pins Ten at 101.
type e2eGoneNexusSource struct {
	e2eNexusSource
	rev4 bool
}

func (s e2eGoneNexusSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	c, err := s.e2eNexusSource.ResolveGameCollection(ctx, gameSourceID, ref)
	if s.rev4 {
		c.Revision = 4
		c.Items[0].FileID = "101"
		return c, err
	}
	c.Items[0].ModID = ""
	return c, err
}

func (e2eGoneNexusSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID != "10" {
		return nil, nil
	}
	return []domain.DownloadableFile{{ID: "101", Name: "Ten Two", FileName: "ten-2.zip", Version: "2.0", Category: "MAIN", IsPrimary: true}}, nil
}

// newE2EGonePinFixture is the NexusMods collection fixture whose Ten pin
// is gone.
func newE2EGonePinFixture(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2ENexusCollectionFixture(t)
	src, err := f.Svc.GetSource("nexusmods")
	require.NoError(t, err)
	f.Svc.RegisterSource(e2eGoneNexusSource{e2eNexusSource: src.(e2eNexusSource)})
	return f
}

// TestE2E_NexusCollection_KeepItSkipped (#657 review): a mod the import
// skipped, which the collection's next revision pins at an available
// file, is listed for attention and comes back by default; its "Keep it
// skipped" box re-plans keeping it out, and the update records the
// declined pin.
func TestE2E_NexusCollection_KeepItSkipped(t *testing.T) {
	f := newE2EGonePinFixture(t)
	ctx := t.Context()
	plan, err := f.Svc.PlanSourceCollectionImport(ctx, f.Game, core.SourceCollectionImportRequest{
		SourceID: "nexusmods", Ref: "abcdef", GonePins: core.GonePinChoices{Skip: []string{"nexusmods:10"}}})
	require.NoError(t, err)
	_, err = f.Svc.ApplyCollectionImport(ctx, f.Game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	src, err := f.Svc.GetSource("nexusmods")
	require.NoError(t, err)
	gone := src.(e2eGoneNexusSource)
	gone.rev4 = true
	f.Svc.RegisterSource(gone)
	entry := filepath.Join(f.Svc.GlobalCacheDir(), f.Game.ID, "collections", "nexusmods-abcdef", "4")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "collection.json"), []byte(strings.Replace(e2eNexusRecipe, `"FileID": "100"`, `"FileID": "101"`, 1)), 0o644))

	var before string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openProfilesModal(),
		clickWhenSettled(`[data-action="update-collection"][data-profile="Three Mods"]`),
		chromedp.WaitVisible(`[data-testid="update-reoffered"]`, chromedp.ByQuery),
		textContent(`[data-testid="update-reoffered"]`, &before),
		clickWhenSettled(`[data-reoffered="nexusmods:10"] [data-testid="update-keep-skipped"]`),
		pollUntil(`document.querySelector('[data-reoffered="nexusmods:10"]')?.dataset.keep === "true"`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, before, "previously skipped because its pinned file was gone; the collection now pins file 101 (version 1.0), which is available")
	assert.Contains(t, before, "will be added")
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(ctx, f.Game.ID, "Three Mods")
		return err == nil && p.Collection != nil && p.Collection.Version == 4
	}, 15*time.Second, 50*time.Millisecond, "the update applied")
	p, err := f.Svc.NewProfileManager().Get(ctx, f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, p.FindRef("nexusmods", "10"), "kept skipped")
	require.NotNil(t, p.CollectionBaseline)
	require.Len(t, p.CollectionBaseline.Skipped, 1)
	assert.True(t, p.CollectionBaseline.Skipped[0].Declined, "the declined pin is recorded")
	assert.Empty(t, f.BrowserErrors())
}
