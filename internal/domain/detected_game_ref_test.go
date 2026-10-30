package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #545: a detected game's identity is the detector that found it plus that
// detector's own id, not a Steam app id. A row that names only its Steam
// app id - every row built before the detector existed, in a test or a
// caller - still reads as a Steam row.
func TestDetectedGame_Ref(t *testing.T) {
	tests := []struct {
		name string
		game domain.DetectedGame
		want string
	}{
		{"steam", domain.DetectedGame{Detector: domain.DetectorSteam, DetectorID: "489830", SteamAppID: "489830"}, "steam:489830"},
		{"battlenet", domain.DetectedGame{Detector: domain.DetectorBattleNet, DetectorID: "wow_classic"}, "battlenet:wow_classic"},
		{"steam app id only", domain.DetectedGame{SteamAppID: "489830"}, "steam:489830"},
		{"nothing", domain.DetectedGame{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.game.Ref())
		})
	}
}

// ParseDetectedRef reads what `lmm game add --from-detected` and POST
// /api/v1/games' from_detected take: "<detector>:<id>", or a bare Steam app
// id, which is what --from-detected always took.
func TestParseDetectedRef(t *testing.T) {
	tests := []struct {
		in           string
		detector, id string
	}{
		{"489830", "steam", "489830"},
		{" 489830 ", "steam", "489830"},
		{"steam:489830", "steam", "489830"},
		{"battlenet:wow_classic", "battlenet", "wow_classic"},
		{"BattleNet:wow", "battlenet", "wow"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			detector, id, err := domain.ParseDetectedRef(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.detector, detector)
			assert.Equal(t, tt.id, id)
		})
	}
	for _, bad := range []string{"", "  ", "wow_classic", "battlenet:", ":wow", "steam:abc"} {
		t.Run("refuses "+bad, func(t *testing.T) {
			_, _, err := domain.ParseDetectedRef(bad)
			assert.ErrorIs(t, err, domain.ErrInvalidDetectedRef)
		})
	}
}
