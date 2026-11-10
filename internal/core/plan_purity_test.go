package core_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planWorld is everything a Plan may read and an Apply may write: the
// installed-mod rows of every profile, and the bytes under the config dir
// (games.yaml, the profile YAMLs), the game's mod directory and its cache.
type planWorld struct {
	rows  map[string][]domain.InstalledMod
	trees map[string]map[string]string
}

// treeContents maps each entry under root to its content - a file's bytes
// or a link's target - so a write, a removal or a relink anywhere under it
// changes the map. A root that does not exist yet is empty: a Plan that
// creates it is caught by its appearing.
func treeContents(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			out[rel] = "link:" + target
			return err
		case d.IsDir():
			out[rel] = "dir"
		default:
			data, err := os.ReadFile(path)
			out[rel] = "file:" + string(data)
			return err
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out
	}
	require.NoError(t, err)
	return out
}

func snapshotPlanWorld(t *testing.T, svc *core.Service, game *domain.Game) planWorld {
	t.Helper()
	ctx := context.Background()
	w := planWorld{rows: map[string][]domain.InstalledMod{}, trees: map[string]map[string]string{
		"config": treeContents(t, svc.ConfigDir()),
		"game":   treeContents(t, game.ModPath),
		"cache":  treeContents(t, svc.GetGameCachePath(game)),
	}}
	names, err := svc.NewProfileManager().ListNames(ctx, game.ID)
	require.NoError(t, err)
	for _, name := range append(names, "default") {
		mods, err := svc.GetInstalledMods(ctx, game.ID, name)
		require.NoError(t, err)
		w.rows[name] = mods
	}
	return w
}

// assertPlanIsPure runs plan and asserts it performed zero mutations: no
// installed-mod row of any of the game's profiles, and no file under the
// config dir (games.yaml, every profile YAML), the game's mod directory or
// its cache changed. It is the "pure computation" half of Plan/Apply that
// callers depend on - a frontend may plan speculatively (to render a
// confirmation prompt) and discard the result. A Plan that downloads,
// caches, deploys or saves anything fails it.
func assertPlanIsPure(t *testing.T, svc *core.Service, game *domain.Game, plan func() error) {
	t.Helper()
	before := snapshotPlanWorld(t, svc, game)
	require.NoError(t, plan())
	after := snapshotPlanWorld(t, svc, game)

	assert.Equal(t, before.rows, after.rows, "planning must not touch the installed-mod rows")
	for name, tree := range before.trees {
		assert.Equal(t, tree, after.trees[name], "planning must not touch the %s tree", name)
	}
}
