package config_test

// #616: a profile's `plugins:` block - the plugin load order and its
// LOOT-userlist-shaped rules. A top-level, omitempty key: a profile without
// one is unchanged, an entry is enabled unless it says `enabled: false`,
// and an in-place save adds or removes only that key.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func pluginsFixture() domain.ProfilePlugins {
	return domain.ProfilePlugins{
		Order: []domain.PluginEntry{
			{Name: "Unofficial Skyrim Special Edition Patch.esp", Enabled: true},
			{Name: "SkyUI_SE.esp", Enabled: true},
			{Name: "Optional Thing.esp", Enabled: false},
		},
		Rules: domain.PluginRules{
			Groups:  []domain.PluginGroup{{Name: "Late Loaders", After: []string{"default"}}},
			Plugins: []domain.PluginRule{{Name: "Patch.esp", Group: "Late Loaders", After: []string{"SkyUI_SE.esp"}, Req: []string{"Base.esm"}}},
		},
	}
}

func TestSaveProfile_PluginsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{
		Name: "p", GameID: "tg",
		Mods:    []domain.ModReference{{SourceID: "local", ModID: "a", Version: "1"}},
		Plugins: pluginsFixture(),
	}
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, dir+"/games/tg/profiles/p.yaml")
	require.Equal(t, 1, strings.Count(got, "enabled:"), "only the disabled entry records the key:\n%s", got)
	require.Contains(t, got, "enabled: false")

	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, profile.Plugins, reloaded.Plugins)
}

func TestLoadProfile_PluginsEntryWithoutEnabledIsEnabled(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "p", "name: p\ngame_id: tg\nmods: []\nplugins:\n  order:\n    - name: A.esp\n    - name: B.esp\n      enabled: true\n    - name: C.esp\n      enabled: false\n")
	profile, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, []domain.PluginEntry{{Name: "A.esp", Enabled: true}, {Name: "B.esp", Enabled: true}, {Name: "C.esp", Enabled: false}}, profile.Plugins.Order)
	require.True(t, profile.Plugins.Rules.IsZero())
}

func TestSaveProfile_PluginsAreEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.True(t, profile.Plugins.IsZero(), "a file without the key has no plugin list")

	profile.Plugins = pluginsFixture()
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, path)
	require.True(t, strings.HasPrefix(got, handEdited), "the author's text is kept, the key appended:\n%s", got)
	require.Contains(t, got, "plugins:\n")

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.Plugins, reloaded.Plugins)

	reloaded.Plugins = domain.ProfilePlugins{}
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing the list restores the file")
}

func TestExportProfileValue_CarriesPlugins(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "tg", Plugins: pluginsFixture()}
	exported := config.ExportProfileValue(profile)
	require.Equal(t, profile.Plugins, exported.Plugins)
	exported.Plugins.Order[0].Name = "changed"
	require.Equal(t, "Unofficial Skyrim Special Edition Patch.esp", profile.Plugins.Order[0].Name, "the export holds its own copy")
	back := config.ProfileFromExported(exported)
	require.Equal(t, exported.Plugins, back.Plugins)
}
