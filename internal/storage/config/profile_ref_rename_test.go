package config_test

// #593: a re-link renames its profile reference in place - same index,
// same text but for the keys that change, every other field carried.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func TestRenameProfileModRef_EditsTheIdentityInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)

	_, err := config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "local", FromModID: "beta", ToSourceID: "nexusmods", ToModID: "42", Version: "2.0",
	})
	require.NoError(t, err)
	want := strings.Replace(handEdited,
		"  - source_id: local\n    mod_id: beta   # the one I care about\n    version: \"1.0\"\n",
		"  - source_id: nexusmods\n    mod_id: \"42\"   # the one I care about\n    version: \"2.0\"\n", 1)
	require.Equal(t, want, readFile(t, path), "only the renamed reference's changed keys move")

	_, err = config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "local", FromModID: "alpha", ToSourceID: "local", ToModID: "omega", Version: "1.0",
	})
	require.NoError(t, err)
	want = strings.Replace(want, `{source_id: local, mod_id: alpha, version: "1.0"}`, `{source_id: local, mod_id: omega, version: "1.0"}`, 1)
	require.Equal(t, want, readFile(t, path), "a flow reference is renamed in place too")

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	var keys []string
	for _, ref := range profile.Mods {
		keys = append(keys, domain.ModKey(ref.SourceID, ref.ModID))
	}
	require.Equal(t, []string{"local:omega", "nexusmods:42", "local:gamma"}, keys, "every reference keeps its index")
}

func TestRenameProfileModRef_CarriesEveryOtherField(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.SaveProfile(dir, &domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{
		{SourceID: "local", ModID: "a", Version: "1"},
		{SourceID: "local", ModID: "b", Version: "1", FileIDs: []string{"f1", "f2"}, Locked: true, Disabled: true, Category: "UI"},
		{SourceID: "local", ModID: "c", Version: "1"},
	}}))

	_, err := config.RenameProfileModRef(dir, "tg", "p", config.ModRefRename{
		FromSourceID: "local", FromModID: "b", ToSourceID: "curseforge", ToModID: "7", Version: "1",
	})
	require.NoError(t, err)

	profile, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, []domain.ModReference{
		{SourceID: "local", ModID: "a", Version: "1"},
		{SourceID: "curseforge", ModID: "7", Version: "1", FileIDs: []string{"f1", "f2"}, Locked: true, Disabled: true, Category: "UI"},
		{SourceID: "local", ModID: "c", Version: "1"},
	}, profile.Mods)
}

func TestRenameProfileModRef_RenamesEveryCopy(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.SaveProfile(dir, &domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{
		{SourceID: "local", ModID: "a", Version: "1"},
		{SourceID: "local", ModID: "b", Version: "1"},
		{SourceID: "local", ModID: "a", Version: "1", Disabled: true},
	}}))

	_, err := config.RenameProfileModRef(dir, "tg", "p", config.ModRefRename{
		FromSourceID: "local", FromModID: "a", ToSourceID: "local", ToModID: "z", Version: "2",
	})
	require.NoError(t, err)

	profile, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, []domain.ModReference{
		{SourceID: "local", ModID: "z", Version: "2"},
		{SourceID: "local", ModID: "b", Version: "1"},
		{SourceID: "local", ModID: "z", Version: "2", Disabled: true},
	}, profile.Mods)
}

func TestRenameProfileModRef_Refusals(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)

	_, err := config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "local", FromModID: "missing", ToSourceID: "local", ToModID: "new", Version: "1",
	})
	require.ErrorIs(t, err, domain.ErrModNotFound, "an old identity the profile does not list")

	_, err = config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "local", FromModID: "alpha", ToSourceID: "local", ToModID: "gamma", Version: "1",
	})
	require.ErrorIs(t, err, config.ErrModRefExists, "a new identity the profile already lists")

	require.Equal(t, handEdited, readFile(t, path), "a refused rename writes nothing")
}
