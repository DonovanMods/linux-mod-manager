package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// #368: a detected game with Workshop items already downloaded is moddable
// by definition, so the default detect listing must show it even though no
// known-games entry covers it. Listable is that rule, in one place, so the
// CLI listing, GET /api/v1/games/detect and the web first-run list cannot
// disagree about which rows a user sees without asking for more.
func TestDetectedGame_Listable(t *testing.T) {
	tests := []struct {
		name string
		game domain.DetectedGame
		want bool
	}{
		{"curated", domain.DetectedGame{Known: true}, true},
		{"curated with workshop items", domain.DetectedGame{Known: true, WorkshopItems: 30}, true},
		{"uncurated with workshop items", domain.DetectedGame{WorkshopItems: 30}, true},
		{"uncurated with an empty-stub workshop manifest", domain.DetectedGame{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.game.Listable())
		})
	}
}

// Addable is the SELECTION half of the same question (#368): a detect
// prompt can configure a curated row from its known-games entry, and an
// uncurated one only when detection already prefilled a source map for it
// - which today means #269's Workshop mapping. Anything else has to go
// through `lmm game add --from-detected`, which asks for the source.
func TestDetectedGame_Addable(t *testing.T) {
	tests := []struct {
		name string
		game domain.DetectedGame
		want bool
	}{
		{"curated", domain.DetectedGame{Known: true}, true},
		{"uncurated with a prefilled source map", domain.DetectedGame{
			WorkshopItems: 30, Sources: map[string]string{"steamworkshop": "1133870"},
		}, true},
		{"uncurated with no source at all", domain.DetectedGame{}, false},
		{"uncurated counted but not mapped (--no-workshop)", domain.DetectedGame{WorkshopItems: 30}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.game.Addable())
		})
	}
}
