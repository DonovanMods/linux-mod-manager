package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #368 review Important 1: a bare number that is BOTH a valid row number and
// some other listed row's Steam app id used to resolve as the row, silently.
// Steam's back catalogue occupies the low integers (Counter-Strike is app id
// 10, Team Fortress Classic 20, Half-Life 70, Portal 400) and a wide listing
// routinely has that many rows, so "10" was a real collision on the owner's
// own machine - and when the collided row is an already-configured CURATED
// one, the repair path overwrites its games.yaml entry and resets its default
// profile's mod list. These tests pin the grammar that cannot be ambiguous.

// ambiguousDetectScan is the owner's #368 listing shape reduced to the
// collision: ten curated rows (row 10 already configured, with a mod in its
// default profile) plus one uncurated Workshop-bearing row whose Steam app
// id is "10" - the same digits as row 10's number, and printed right beside
// it in the listing.
func ambiguousDetectScan(t *testing.T) []domain.DetectedGame {
	t.Helper()
	install := t.TempDir()
	scan := make([]domain.DetectedGame, 0, 11)
	for i := 1; i <= 10; i++ {
		scan = append(scan, domain.DetectedGame{
			SteamAppID:  fmt.Sprintf("10000%d", i),
			Slug:        fmt.Sprintf("curated-%d", i),
			Name:        fmt.Sprintf("Curated %d", i),
			InstallPath: install,
			NexusID:     fmt.Sprintf("curated%d", i),
			Known:       true,
		})
	}
	return append(scan, domain.DetectedGame{
		SteamAppID: "10", Slug: "counter-strike", Name: "Counter-Strike",
		InstallPath: install, Sources: map[string]string{"steamworkshop": "10"},
		WorkshopItems: 4,
	})
}

// configureCuratedTen writes the games.yaml entry and default profile a user
// already has for row 10, with a hand-edited name and one installed mod - the
// two things an unasked-for repair destroys.
func configureCuratedTen(t *testing.T, installPath string) {
	t.Helper()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{
		ID: "curated-10", Name: "Curated Ten (hand-edited)", InstallPath: installPath,
		ModPath: installPath + "/mods", SourceIDs: map[string]string{"nexusmods": "curated10"},
	}))
	require.NoError(t, config.SaveProfile(configDir, &domain.Profile{
		Name: "default", GameID: "curated-10", IsDefault: true,
		Mods: []domain.ModReference{{SourceID: "nexusmods", ModID: "999", Version: "1.0.0"}},
	}))
}

// TestDoGameDetect_AmbiguousBareNumberIsRefusedAndWritesNothing is the
// owner's #368 scenario: "10" is row 10 and also the app id printed beside
// row 11. Resolving it either way configures a game the user did not name, so
// it is refused - and the configured row 10 keeps both its games.yaml entry
// and its default profile's mod list.
func TestDoGameDetect_AmbiguousBareNumberIsRefusedAndWritesNothing(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	scan := ambiguousDetectScan(t)
	configureCuratedTen(t, scan[9].InstallPath)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("10\n")), svc, scan, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `ambiguous selection: "10"`)
	assert.Contains(t, err.Error(), "row 10 (Curated 10)")
	assert.Contains(t, err.Error(), "Counter-Strike")
	assert.Contains(t, err.Error(), `"#10"`, "the error names the explicit row form")
	assert.Contains(t, err.Error(), `"app:10"`, "and the explicit app-id form")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "curated-10")
	assert.Equal(t, "Curated Ten (hand-edited)", saved["curated-10"].Name,
		"a refused selection never replays the repair path's games.yaml overwrite")
	assert.NotContains(t, saved, "counter-strike", "and adds nothing either")

	profile, err := config.LoadProfile(configDir, "curated-10", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1, "the default profile's mod list survives")
	assert.Equal(t, "999", profile.Mods[0].ModID)
}

// TestDoGameDetect_ExplicitRowFormResolvesACollision: "#10" is row 10, always
// - which for an already-configured curated row is the documented repair.
func TestDoGameDetect_ExplicitRowFormResolvesACollision(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	scan := ambiguousDetectScan(t)
	configureCuratedTen(t, scan[9].InstallPath)
	cmd, buf := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("#10\n")), svc, scan, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Added: Curated 10 (curated-10)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "curated-10")
	assert.Equal(t, "Curated 10", saved["curated-10"].Name, "an explicit row selection repairs it")
	assert.NotContains(t, saved, "counter-strike")
}

// TestDoGameDetect_ExplicitAppIDFormResolvesACollision: "app:10" is the row
// whose Steam app id is 10, always - never row 10.
func TestDoGameDetect_ExplicitAppIDFormResolvesACollision(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	scan := ambiguousDetectScan(t)
	configureCuratedTen(t, scan[9].InstallPath)
	cmd, buf := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("app:10\n")), svc, scan, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Added: Counter-Strike (counter-strike)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "counter-strike")
	assert.Equal(t, "Curated Ten (hand-edited)", saved["curated-10"].Name,
		"the collided row number is untouched")
}

// TestDoGameDetect_UnambiguousSelectionsStillResolve: the collision refusal is
// narrow. A bare number no listed row claims as an app id is still that row,
// and a bare app id no row number can be is still that app id.
func TestDoGameDetect_UnambiguousSelectionsStillResolve(t *testing.T) {
	configDir = t.TempDir()
	svc := workshopDetectService(t)
	cmd, _ := newDetectCmd(t)

	err := doGameDetect(context.Background(), cmd,
		bufio.NewReader(strings.NewReader("2, 100003\n")), svc, ambiguousDetectScan(t), nil)
	require.NoError(t, err)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "curated-2", "a bare row number nothing claims as an app id")
	assert.Contains(t, saved, "curated-3", "a bare app id no row number can be")
}

// TestGameDetectSelector_Grammar pins the whole grammar at the unit the
// prompt, --select and the ambiguity refusal all share.
func TestGameDetectSelector_Grammar(t *testing.T) {
	games := ambiguousDetectScan(t)

	tests := []struct {
		name    string
		part    string
		want    int
		wantErr string
	}{
		{name: "bare row number", part: "2", want: 2},
		{name: "bare app id", part: "100003", want: 3},
		{name: "collision is refused", part: "10", wantErr: "ambiguous selection"},
		{name: "explicit row wins on a collision", part: "#10", want: 10},
		{name: "explicit app id wins on a collision", part: "app:10", want: 11},
		{name: "explicit row form of an unambiguous number", part: "#2", want: 2},
		{name: "explicit app id form of an unambiguous app id", part: "app:100003", want: 3},
		{name: "explicit row out of range", part: "#99", wantErr: "invalid selection"},
		{name: "explicit app id nothing carries", part: "app:999999", wantErr: "invalid selection"},
		{name: "neither a row nor an app id", part: "999999", wantErr: "invalid selection"},
		{name: "not a number at all", part: "skyrim", wantErr: "invalid selection"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, err := gameDetectSelector(tc.part, games)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, n)
		})
	}
}

// TestGameDetectSelector_ARowThatIsItsOwnAppIDIsNotAmbiguous: both readings
// name the same row, so there is nothing for the user to disambiguate.
func TestGameDetectSelector_ARowThatIsItsOwnAppIDIsNotAmbiguous(t *testing.T) {
	games := []domain.DetectedGame{
		{SteamAppID: "7", Slug: "one", Name: "One", Known: true},
		{SteamAppID: "2", Slug: "two", Name: "Two", Known: true},
	}

	n, err := gameDetectSelector("2", games)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}
