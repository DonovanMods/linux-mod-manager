package config_test

// #592: a mod reference's user category lives in the profile document, as
// `category:` - absent means "no user category", so every file written
// before the key existed is unchanged, and an in-place save adds or removes
// only that key.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func TestSaveProfile_CategoryIsEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	want := handEdited

	step := func(name string, change func(p *domain.Profile), edit func(string) string) {
		t.Helper()
		profile, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err, name)
		change(profile)
		require.NoError(t, config.SaveProfile(dir, profile), name)
		want = edit(want)
		require.Equal(t, want, readFile(t, path), name)
		reloaded, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err, name)
		require.Equal(t, profile.Mods, reloaded.Mods, name)
	}
	byID := func(p *domain.Profile, id string) *domain.ModReference { return p.FindRef("local", id) }

	step("categorise beta", func(p *domain.Profile) {
		byID(p, "beta").Category = "User Interface"
	}, func(s string) string {
		return strings.Replace(s, "    version: \"1.0\"\n\n", "    version: \"1.0\"\n    category: User Interface\n\n", 1)
	})
	step("categorise alpha (flow)", func(p *domain.Profile) {
		byID(p, "alpha").Category = "QoL: misc"
	}, func(s string) string {
		return strings.Replace(s, `version: "1.0"}`, `version: "1.0", category: 'QoL: misc'}`, 1)
	})
	step("recategorise beta", func(p *domain.Profile) {
		byID(p, "beta").Category = "UI"
	}, func(s string) string {
		return strings.Replace(s, "category: User Interface\n", "category: UI\n", 1)
	})
	step("clear beta", func(p *domain.Profile) {
		byID(p, "beta").Category = ""
	}, func(s string) string {
		return strings.Replace(s, "    category: UI\n", "", 1)
	})
	step("clear alpha", func(p *domain.Profile) {
		byID(p, "alpha").Category = ""
	}, func(s string) string {
		return strings.Replace(s, `, category: 'QoL: misc'}`, `}`, 1)
	})
	require.Equal(t, handEdited, want, "clearing every category restores the file")
}

func TestSaveProfile_WholeWriteCarriesCategory(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{
		{SourceID: "local", ModID: "a", Version: "1", Category: "Gameplay"},
		{SourceID: "local", ModID: "b", Version: "1"},
	}}
	require.NoError(t, config.SaveProfile(dir, profile))
	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, "Gameplay", reloaded.Mods[0].Category)
	require.Empty(t, reloaded.Mods[1].Category)
	path, err := config.ProfilePath(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(readFile(t, path), "category:"), "an empty category writes no key")
}
