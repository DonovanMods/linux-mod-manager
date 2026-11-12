package bethesda

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdapterIdentity(t *testing.T) {
	a := New()
	assert.Equal(t, "bethesda", a.ID())
	assert.NotEmpty(t, a.Label())
}

// A game lmm recognises gets no findings.
func TestVerify_KnownGameIsQuiet(t *testing.T) {
	findings, err := adapter.Verify(context.Background(), New(), adapter.VerifyRequest{Game: &domain.Game{ID: "skyrim-se"}})
	require.NoError(t, err)
	assert.Empty(t, findings)
}

// A game set to `adapter: bethesda` that the adapter cannot identify gets
// one note - it counts towards neither tally, so it never changes verify's
// exit code - naming the ids it does recognise.
func TestVerify_UnknownGameIsANote(t *testing.T) {
	findings, err := adapter.Verify(context.Background(), New(), adapter.VerifyRequest{Game: &domain.Game{ID: "my-skyrim"}})
	require.NoError(t, err)
	require.Len(t, findings, 1)
	f := findings[0]
	assert.Equal(t, StatusGameUnknown, f.Status)
	assert.Equal(t, adapter.SeverityNote, f.Severity)
	assert.False(t, f.Fixable)
	for _, slug := range []string{"skyrim-se", "skyrim-vr", "fallout4", "fallout4-vr", "starfield", "skyrim"} {
		assert.Contains(t, f.Note, slug)
	}
	assert.Contains(t, f.Note, "my-skyrim")
}

// #605's PrefixConsumer: every Tier 1 game keeps Plugins.txt in its prefix,
// so it needs one; a game the adapter cannot identify has no plugin rules,
// so lmm writes nothing there and needs none (its bethesda_game_unknown
// note says why).
func TestUsesPrefix(t *testing.T) {
	for _, slug := range []string{"skyrim-se", "skyrim-vr", "fallout4", "fallout4-vr", "starfield", "skyrim"} {
		assert.True(t, adapter.UsesPrefix(New(), &domain.Game{ID: slug}), slug)
	}
	assert.True(t, adapter.UsesPrefix(New(), &domain.Game{ID: "x", Launch: &domain.GameLaunch{SteamAppID: "489830"}}))
	assert.False(t, adapter.UsesPrefix(New(), &domain.Game{ID: "my-skyrim"}))
	assert.False(t, adapter.UsesPrefix(New(), nil))
}
