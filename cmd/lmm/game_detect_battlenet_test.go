package main

import (
	"bytes"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// battleNetDetectFixture is a Steam row and two Battle.net rows - one
// curated, one no catalog entry covers.
func battleNetDetectFixture() []domain.DetectedGame {
	return []domain.DetectedGame{
		{Detector: "steam", DetectorID: "489830", SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition", Known: true},
		{Detector: "battlenet", DetectorID: "wow_classic", Slug: "wow-classic", Name: "World of Warcraft Classic", InstallPath: "/games/wow/_classic_",
			Sources: map[string]string{"curseforge": "1"}, Known: true},
		{Detector: "battlenet", DetectorID: "osi", Slug: "diablo-ii-resurrected", Name: "Diablo II Resurrected", InstallPath: "/games/d2r"},
	}
}

// #545: a Battle.net row prints its detector reference where a Steam row
// prints its app id - the handle `lmm game add --from-detected` takes.
func TestPrintDetectedGameRow_BattleNetRowPrintsItsReference(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	games := battleNetDetectFixture()
	printDetectedGameRow(cmd, 2, games[1], nil, nil)
	assert.Contains(t, out.String(), "World of Warcraft Classic (wow-classic)  battlenet:wow_classic")
}

// The detect prompt takes a row's reference as it takes an app id, and the
// refusal for a row nothing can configure names the reference to add it by.
func TestGameDetectSelectionIndices_BattleNetReference(t *testing.T) {
	games := battleNetDetectFixture()
	indices, err := gameDetectSelectionIndices("battlenet:wow_classic,489830", games, nil)
	require.NoError(t, err)
	assert.Equal(t, []int{2, 1}, indices)

	_, err = gameDetectSelectionIndices("battlenet:osi", games, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lmm game add --from-detected battlenet:osi")

	_, err = gameDetectSelectionIndices("battlenet:wow", games, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selection")
}
