package core_test

// #605: GameEdit.Prefix and GameEdit.PrefixUser set and clear a game's
// `prefix:` and `prefix_user:` - the edit `lmm game edit --prefix
// / --prefix-user` and PUT /api/v1/games/{id} share.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedPrefixGame(t *testing.T, svc *core.Service) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "skyrim-se", Name: "Skyrim SE", InstallPath: dir, ModPath: dir,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}, LinkMethod: domain.LinkSymlink,
	}))
}

func editPrefix(svc *core.Service, prefix, user *string) (*core.GameListEntry, error) {
	return svc.EditGame(context.Background(), "skyrim-se", core.GameEdit{Prefix: prefix, PrefixUser: user})
}

func TestEditGame_Prefix_SetsAndClears(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := newFlowsTestService(t)
	seedPrefixGame(t, svc)

	prefix, user := "~/Games/Heroic/Prefixes/skyrim/", "dyoung"
	entry, err := editPrefix(svc, &prefix, &user)
	require.NoError(t, err)
	want := filepath.Join(home, "Games/Heroic/Prefixes/skyrim")
	assert.Equal(t, want, entry.Prefix, `"~/" expanded and the path cleaned`)
	assert.Equal(t, "dyoung", entry.PrefixUser)

	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, want, onDisk["skyrim-se"].Prefix)
	assert.Equal(t, "dyoung", onDisk["skyrim-se"].PrefixUser)

	// Another edit leaves both alone.
	name := "Skyrim Special Edition"
	entry, err = svc.EditGame(context.Background(), "skyrim-se", core.GameEdit{Name: &name})
	require.NoError(t, err)
	assert.Equal(t, want, entry.Prefix)

	empty := ""
	entry, err = editPrefix(svc, &empty, &empty)
	require.NoError(t, err)
	assert.Empty(t, entry.Prefix, `"" clears it, so lmm derives the prefix again`)
	assert.Empty(t, entry.PrefixUser)
	onDisk, err = config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Empty(t, onDisk["skyrim-se"].Prefix)
}

// A prefix need not exist yet - `lmm game show` says so - but it must be an
// absolute path, and not a file.
func TestEditGame_Prefix_Refusals(t *testing.T) {
	svc := newFlowsTestService(t)
	seedPrefixGame(t, svc)
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))

	for name, edit := range map[string]core.GameEdit{
		"relative prefix":         {Prefix: ptr("Prefixes/skyrim")},
		"a file":                  {Prefix: &file},
		"a prefix with a space":   {Prefix: ptr(" /pfx/skyrim")},
		"a prefix with a newline": {Prefix: ptr("/pfx/skyrim\n")},
		"a slash in the user":     {PrefixUser: ptr("users/dyoung")},
		"dot-dot user":            {PrefixUser: ptr("..")},
		"a user with a space":     {PrefixUser: ptr("dyoung ")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.EditGame(context.Background(), "skyrim-se", edit)
			var spec *core.GameSpecError
			require.True(t, errors.As(err, &spec), "%v", err)
			if edit.Prefix != nil {
				assert.Equal(t, "prefix", spec.Field)
			} else {
				assert.Equal(t, "prefix_user", spec.Field)
			}
			onDisk, err := config.LoadGames(svc.ConfigDir())
			require.NoError(t, err)
			assert.Empty(t, onDisk["skyrim-se"].Prefix)
			assert.Empty(t, onDisk["skyrim-se"].PrefixUser)
		})
	}

	missing := filepath.Join(t.TempDir(), "not-yet")
	entry, err := editPrefix(svc, &missing, nil)
	require.NoError(t, err, "a prefix the game has not created yet is accepted")
	assert.Equal(t, missing, entry.Prefix)
}

func ptr[T any](v T) *T { return &v }
