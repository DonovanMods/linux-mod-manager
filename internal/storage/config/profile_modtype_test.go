package config_test

// #607: a mod reference's stated mod type (a collection's Vortex
// details.type, "dinput" for a script extender) lives in the profile
// document as `mod_type:`. Absent means the game's default, so every file
// written before the key existed is unchanged, and an in-place save adds or
// removes only that key.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func TestSaveProfile_ModTypeIsEditedInPlace(t *testing.T) {
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

	step("type beta", func(p *domain.Profile) {
		byID(p, "beta").ModType = "dinput"
	}, func(s string) string {
		return strings.Replace(s, "    version: \"1.0\"\n\n", "    version: \"1.0\"\n    mod_type: dinput\n\n", 1)
	})
	step("type alpha (flow)", func(p *domain.Profile) {
		byID(p, "alpha").ModType = "enb"
	}, func(s string) string {
		return strings.Replace(s, `version: "1.0"}`, `version: "1.0", mod_type: enb}`, 1)
	})
	step("clear beta", func(p *domain.Profile) {
		byID(p, "beta").ModType = ""
	}, func(s string) string {
		return strings.Replace(s, "    mod_type: dinput\n", "", 1)
	})
	step("clear alpha", func(p *domain.Profile) {
		byID(p, "alpha").ModType = ""
	}, func(s string) string {
		return strings.Replace(s, `, mod_type: enb}`, `}`, 1)
	})
	require.Equal(t, handEdited, want, "clearing every type restores the file")
}

func TestSaveProfile_WholeWriteAndCollectionCarryModType(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{
		{SourceID: "nexusmods", ModID: "30379", Version: "2.2.6", ModType: "dinput"},
		{SourceID: "nexusmods", ModID: "2", Version: "1"},
	}}
	require.NoError(t, config.SaveProfile(dir, profile))
	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, "dinput", reloaded.Mods[0].ModType)
	require.Empty(t, reloaded.Mods[1].ModType)
	path, err := config.ProfilePath(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(readFile(t, path), "mod_type:"), "an empty type writes no key")

	doc := &domain.CollectionDocument{FormatVersion: 1, Name: "c", Requires: []string{"mod_type:dinput"},
		Items: []domain.CollectionItem{{Ref: profile.Mods[0], Name: "SKSE64"}}}
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	require.Contains(t, string(data), "mod_type: dinput")
	back, err := config.ParseCollection(data)
	require.NoError(t, err)
	require.Equal(t, "dinput", back.Items[0].Ref.ModType)
}
