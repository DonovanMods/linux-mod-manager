package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
	"github.com/stretchr/testify/require"
)

// #315: the conflict list is grouped per owning mod by every renderer (the
// CLI's "From <mod> (<id>):" block, `--json`'s details.conflicts), so core
// must hand it over already grouped: owning mod key first, path second.
// The DB answers ordered by path alone, which interleaves two owners.
func TestGetConflictsSortedByOwningModThenPath(t *testing.T) {
	tempDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	c := cache.New(tempDir)
	inst := core.NewInstaller(c, linker.New(domain.LinkSymlink), database)

	game := &domain.Game{ID: "test-game", ModPath: filepath.Join(tempDir, "mods")}
	require.NoError(t, os.MkdirAll(game.ModPath, 0755))

	// Two owners whose paths interleave under a path-only sort:
	//   a.txt -> zzz, b.txt -> aaa, c.txt -> zzz, d.txt -> aaa
	for _, owner := range []struct {
		id    string
		files []string
	}{
		{"zzz", []string{"a.txt", "c.txt"}},
		{"aaa", []string{"b.txt", "d.txt"}},
	} {
		for _, f := range owner.files {
			require.NoError(t, c.Store(game.ID, "nexusmods", owner.id, "1.0", f, []byte(owner.id)))
		}
		require.NoError(t, inst.Install(context.Background(), game,
			&domain.Mod{SourceID: "nexusmods", ID: owner.id, Version: "1.0", GameID: game.ID}, "default"))
	}

	// The incoming mod claims all four.
	for _, f := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		require.NoError(t, c.Store(game.ID, "nexusmods", "new", "1.0", f, []byte("new")))
	}
	conflicts, err := inst.GetConflicts(context.Background(), game,
		&domain.Mod{SourceID: "nexusmods", ID: "new", Version: "1.0", GameID: game.ID}, "default")
	require.NoError(t, err)

	got := make([]string, 0, len(conflicts))
	for _, cf := range conflicts {
		got = append(got, cf.CurrentModID+"/"+cf.RelativePath)
	}
	want := []string{"aaa/b.txt", "aaa/d.txt", "zzz/a.txt", "zzz/c.txt"}
	require.Equal(t, want, got)
}
