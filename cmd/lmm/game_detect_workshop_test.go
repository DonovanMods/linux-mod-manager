package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #368: `lmm game detect` listed the Workshop-bearing games behind
// --include-unknown, numbered only the curated rows, and rejected the Steam
// app id its own hint told the user to copy. These tests drive the whole
// interactive flow against a hand-built scan (no real Steam library) and a
// service holding the built-in steamworkshop source, which is what
// core.AddGame validates a prefilled `steamworkshop:` mapping against.

// workshopDetectService is newGameDetectTestService plus the built-in
// steamworkshop source registered, so an uncurated row's prefilled source
// map resolves. No network is reached: AddGame only asks the registry
// whether the id exists.
//
// Built through testutil.WorkshopOptions, the one door
// steamworkshop's own TestEveryTestOutsideThisPackageBuildsTheSourceThroughTheGuardedHelper
// (#347) allows, which refuses an empty BaseURL - that would silently be
// Valve's production Web API. The server it is pointed at fails the test if
// anything asks it for anything, which is what "no network is reached"
// above is worth asserting rather than claiming.
func workshopDetectService(t *testing.T) *core.Service {
	t.Helper()
	svc := newGameDetectTestService(t)
	svc.RegisterSource(steamworkshop.New(testutil.WorkshopOptions(t, workshopDetectNoCallServer(t).URL)))
	return svc
}

// workshopDetectNoCallServer stands in for Valve's Web API and fails the
// test on any request: detection never fetches, it only asks the registry
// whether the source id exists.
func workshopDetectNoCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the detect flow reached the Steam Workshop API: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// workshopDetectScan is the shape the owner's machine produced: one curated
// game, one uncurated game with Workshop items, and one plain uncurated game
// with nothing to say for itself. None of them is configured - a test that
// needs a configured row writes it itself.
func workshopDetectScan(t *testing.T) []domain.DetectedGame {
	t.Helper()
	install := t.TempDir()
	return []domain.DetectedGame{
		{SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition",
			InstallPath: install, NexusID: "skyrimspecialedition", Known: true},
		{SteamAppID: "1133870", Slug: "space-engineers-2", Name: "Space Engineers 2",
			InstallPath: install, Sources: map[string]string{"steamworkshop": "1133870"}, WorkshopItems: 30},
		{SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory", InstallPath: install},
	}
}

// newDetectCmd is a capture-buffered command PLUS a reset of every
// package-level flag doGameDetect reads. The detect flags are cobra
// globals, so a sibling test that leaves --all set makes these read as
// non-interactive and never print the prompt at all.
func newDetectCmd(t *testing.T) (*cobra.Command, *strings.Builder) {
	t.Helper()
	oldAll, oldSelect, oldUnknown, oldJSON := gameDetectAll, gameDetectSelect, gameDetectIncludeUnknown, jsonOutput
	gameDetectAll, gameDetectSelect, gameDetectIncludeUnknown, jsonOutput = false, "", false, false
	t.Cleanup(func() {
		gameDetectAll, gameDetectSelect, gameDetectIncludeUnknown, jsonOutput = oldAll, oldSelect, oldUnknown, oldJSON
	})
	buf := &strings.Builder{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	return cmd, buf
}

// TestDoGameDetect_ListsWorkshopRowsByDefaultAndNumbersThemContinuously is
// the owner's hand test: the Workshop-bearing game is in the plain listing,
// numbered after the curated rows rather than left unnumbered, and every
// row that has items says how many. The plain uncurated game still needs
// --include-unknown.
func TestDoGameDetect_ListsWorkshopRowsByDefaultAndNumbersThemContinuously(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("none\n")), svc, workshopDetectScan(t), nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Found 2 moddable game(s):")
	assert.Contains(t, out, "1. Skyrim Special Edition (skyrim-se)")
	assert.Contains(t, out, "2. Space Engineers 2 (space-engineers-2)")
	assert.Contains(t, out, "app id 1133870")
	assert.Contains(t, out, "Steam Workshop: 30 items")
	assert.NotContains(t, out, "Satisfactory", "a plain uncurated game still needs --include-unknown")
	assert.Contains(t, out, "Add games to config? [1-2/#row/app:<id>/all/none]:")
}

// TestDoGameDetect_SelectsAnUncuratedWorkshopRowByNumber: the row is
// numbered, so its number works - and configuring it runs the same prefill
// `lmm game add --from-detected` runs, source map included.
func TestDoGameDetect_SelectsAnUncuratedWorkshopRowByNumber(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	scan := workshopDetectScan(t)
	cmd, buf := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("2\n")), svc, scan, nil)
	require.NoError(t, err)

	// The prefill itself - the source map, the <install>/mods default, the
	// created profile - is core.ApplyDetectSelection's, pinned by
	// TestApplyDetectSelection_ConfiguresBothKindsOfRowUnderOneSlot. What
	// this test owns is the CLI wiring: the typed row reached that seam and
	// the run said what it added.
	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "space-engineers-2")
	assert.Equal(t, map[string]string{"steamworkshop": "1133870"}, saved["space-engineers-2"].SourceIDs)
	assert.Contains(t, buf.String(), "Added: Space Engineers 2 (space-engineers-2)")
}

// TestDoGameDetect_SelectsBySteamAppID pins the second half of the owner's
// report: the listing prints an app id beside every uncurated row, so the
// prompt accepts one - "2383970, 892970" was a reasonable thing to type and
// used to be an error.
func TestDoGameDetect_SelectsBySteamAppID(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("1133870\n")), svc, workshopDetectScan(t), nil)
	require.NoError(t, err)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "space-engineers-2")
}

// TestDoGameDetect_SelectsAMixOfNumbersAndAppIDs: the two spellings are one
// selection, in the order typed.
func TestDoGameDetect_SelectsAMixOfNumbersAndAppIDs(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("1, 1133870\n")), svc, workshopDetectScan(t), nil)
	require.NoError(t, err)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "skyrim-se")
	assert.Contains(t, saved, "space-engineers-2")
}

// TestDoGameDetect_InvalidSelectionNamesWhatIsAccepted: the old message
// said "use numbers 1-2, all, or none" while the listing right above it
// printed app ids, which is how the owner got told a listed row was
// invalid.
func TestDoGameDetect_InvalidSelectionNamesWhatIsAccepted(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("999999\n")), svc, workshopDetectScan(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid selection: "999999"`)
	assert.Contains(t, err.Error(), "a row number 1-2")
	assert.Contains(t, err.Error(), "a Steam app id")
	assert.Contains(t, err.Error(), "all")
	assert.Contains(t, err.Error(), "none")
}

// TestDoGameDetect_InvalidSelectionOffersOnlyWhatResolves pins #368 review
// Minor 7: typing the app id of a game that IS installed but is hidden by
// the default listing got "use a row number 1-2, a Steam app id, all, or
// none" - naming the spelling the user just used as accepted, which reads as
// a bug in lmm rather than as "that row is not on this list".
func TestDoGameDetect_InvalidSelectionOffersOnlyWhatResolves(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("526870\n")), svc, workshopDetectScan(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a Steam app id from the list above",
		"only a LISTED row's app id resolves")
	assert.Contains(t, err.Error(), "--include-unknown",
		"and the flag that would list the row the user just named")
}

// TestDoGameDetect_InvalidSelectionUnderIncludeUnknownDropsTheFlagHint: with
// the flag already given, every installed game is on the list, so there is
// no "rest" to point at - the selection is simply not a row.
func TestDoGameDetect_InvalidSelectionUnderIncludeUnknownDropsTheFlagHint(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)
	gameDetectIncludeUnknown = true

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("999999\n")), svc, workshopDetectScan(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a Steam app id from the list above")
	assert.NotContains(t, err.Error(), "--include-unknown")
}

// TestDoGameDetect_AllIncludesAnUncuratedWorkshopRow: "all" means every
// listed row that is not already configured, and a Workshop-bearing row is
// listed AND addable - detection filled in its source map.
func TestDoGameDetect_AllIncludesAnUncuratedWorkshopRow(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("all\n")), svc, workshopDetectScan(t), nil)
	require.NoError(t, err)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "skyrim-se")
	assert.Contains(t, saved, "space-engineers-2")
	assert.NotContains(t, saved, "satisfactory", "a row that was never listed is not part of \"all\"")
}

// TestDoGameDetect_JSONAllConfiguresAWorkshopRowWithoutIncludeUnknown pins
// the rule #368 review Minor 5's corrected help and README now state: the
// LISTING document needs --include-unknown, but what --all/--select choose
// from is the DEFAULT listing - which since #368 holds Workshop-bearing
// uncurated rows. So a plain `--json --all` configures one, and emits the
// RESULT document, never a listing.
func TestDoGameDetect_JSONAllConfiguresAWorkshopRowWithoutIncludeUnknown(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)
	withJSONOutput(t)
	gameDetectAll = true

	var doc core.GameDetectResult
	stdout := captureStdout(t, func() error {
		return assertStdinNeverRead(t, func() error {
			return doGameDetect(context.Background(), cmd,
				bufio.NewReader(poisonReader{t}), svc, workshopDetectScan(t), nil)
		})
	})
	decodeSingleDoc(t, stdout, &doc)

	assert.Empty(t, buf.String(), "no console text may sit beside the document")
	assert.Equal(t, []string{"skyrim-se", "space-engineers-2"}, doc.Saved,
		"the default listing's rows, Workshop-bearing uncurated one included")
	assert.NotContains(t, stdout, `"games"`, "a plain --json scan emits no LISTING document")
}

// TestDoGameDetect_UncuratedRowWithNoSourceIsRefusedWithTheAddPath: under
// --include-unknown a game with nothing but an install path is listed and
// numbered, but nothing tells lmm which source it belongs to - so the
// prompt sends the user to the flow that asks, rather than writing an
// unusable games.yaml entry.
func TestDoGameDetect_UncuratedRowWithNoSourceIsRefusedWithTheAddPath(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)
	gameDetectIncludeUnknown = true

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("3\n")), svc, workshopDetectScan(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Satisfactory")
	assert.Contains(t, err.Error(), "lmm game add --from-detected 526870")
	assert.Contains(t, buf.String(), "3. Satisfactory (satisfactory)",
		"the row is still listed and numbered under --include-unknown")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Empty(t, saved, "a refused selection writes nothing")
}

// TestDoGameDetect_DuplicateSelectionIsRefused: with two spellings for one
// row, naming it twice is easy to do by accident and never means anything -
// the second add would fail on ErrGameExists after the first had already
// been written. core.SelectDetectedGames refuses a duplicate for the same
// reason.
func TestDoGameDetect_DuplicateSelectionIsRefused(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("2,1133870\n")), svc, workshopDetectScan(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate selection")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Empty(t, saved)
}

// TestDoGameDetect_ConfiguredUncuratedRowNamesGameEdit pins #368 review
// Minor 3: re-selecting a configured UNCURATED row is refused rather than
// overwritten (unlike a curated row's repair), and --help says to "change it
// with `lmm game edit`" - but what the user saw was the raw wrapped
// ErrGameExists, which names no way forward at all.
func TestDoGameDetect_ConfiguredUncuratedRowNamesGameEdit(t *testing.T) {
	configDir = t.TempDir()
	scan := workshopDetectScan(t)
	// Written BEFORE the service opens: AddGame's duplicate check reads the
	// Service's own loaded games, which is what a real run has - games.yaml
	// is read at open, after the scan.
	require.NoError(t, config.SaveGame(configDir, &domain.Game{
		ID: "space-engineers-2", Name: "Space Engineers 2 (hand-edited)", InstallPath: scan[1].InstallPath,
		ModPath: filepath.Join(scan[1].InstallPath, "mods"), SourceIDs: map[string]string{"steamworkshop": "1133870"},
	}))
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("2\n")), svc, scan, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrGameExists)
	assert.Contains(t, err.Error(), "lmm game edit space-engineers-2")
	assert.Contains(t, buf.String(), "[configured]", "the row said so before it was picked")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Equal(t, "Space Engineers 2 (hand-edited)", saved["space-engineers-2"].Name,
		"a refused add rewrites nothing")
}

// TestDoGameDetect_MixedSelectionSaysWhatItWroteBeforeRefusing: the curated
// half of a selection is applied before the uncurated half, so a refusal
// there must still report what did get written.
func TestDoGameDetect_MixedSelectionSaysWhatItWroteBeforeRefusing(t *testing.T) {
	configDir = t.TempDir()
	scan := workshopDetectScan(t)
	require.NoError(t, config.SaveGame(configDir, &domain.Game{
		ID: "space-engineers-2", Name: "Space Engineers 2", InstallPath: scan[1].InstallPath,
		ModPath: filepath.Join(scan[1].InstallPath, "mods"), SourceIDs: map[string]string{"steamworkshop": "1133870"},
	}))
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("1,2\n")), svc, scan, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lmm game edit space-engineers-2")
	assert.Contains(t, buf.String(), "Added: Skyrim Special Edition (skyrim-se)")
}

// TestDoGameDetect_IncludeUnknownHeaderDoesNotCallEveryGameModdable pins
// #368 review Minor 2: --include-unknown widened the counted set to include
// rows the listing's own next line says are NOT known to be moddable, so the
// header claimed them.
func TestDoGameDetect_IncludeUnknownHeaderDoesNotCallEveryGameModdable(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)
	gameDetectIncludeUnknown = true

	require.NoError(t, doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("none\n")), svc, workshopDetectScan(t), nil))

	out := buf.String()
	assert.Contains(t, out, "Found 3 installed game(s):")
	assert.NotContains(t, out, "moddable game(s)",
		"a row the next line calls unknown is not claimed as moddable")
}

// TestDoGameDetect_ModdableHeaderStaysOnTheDefaultListing: every row the
// default listing keeps IS moddable - curated, or moddable by observation
// (Workshop items) - so the header still says so.
func TestDoGameDetect_ModdableHeaderStaysOnTheDefaultListing(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, buf := newDetectCmd(t)

	require.NoError(t, doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("none\n")), svc, workshopDetectScan(t), nil))
	assert.Contains(t, buf.String(), "Found 2 moddable game(s):")
}

// TestDoGameDetect_CuratedRowShowsItsWorkshopCount: the count is a fact
// about the row, not about the section it is in.
func TestDoGameDetect_CuratedRowShowsItsWorkshopCount(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	scan := []domain.DetectedGame{{
		SteamAppID: "1149460", Slug: "icarus", Name: "Icarus", InstallPath: t.TempDir(),
		Sources: map[string]string{"icarus": "icarus", "steamworkshop": "1149460"}, WorkshopItems: 1, Known: true,
	}}
	cmd, buf := newDetectCmd(t)

	require.NoError(t, doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("none\n")), svc, scan, nil))
	assert.Contains(t, buf.String(), "Steam Workshop: 1 item")
}
