package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prefixTarget spells each existing folder as it is on disk (an exact
// match first), and keeps rel's spelling from the first folder that is not
// there, and for the file's own name.
func TestPrefixTarget_FoldsExistingFolders(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "AppData", "local", "Game"), 0o755))
	assert.Equal(t, filepath.Join(dir, "AppData", "local", "Game", "Mods", "A.pak"), prefixTarget(dir, "AppData/Local/game/Mods/A.pak"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "AppData", "Local"), 0o755))
	assert.Equal(t, filepath.Join(dir, "AppData", "Local", "x"), prefixTarget(dir, "AppData/Local/x"), "an exact match wins")
	assert.Equal(t, filepath.Join(dir, "a.pak"), prefixTarget(dir, "a.pak"))
}

func TestPrefixOfUserDir_InvertsUserDir(t *testing.T) {
	want := &domain.ResolvedPrefix{Path: "/games/pfx", User: "steamuser"}
	got, ok := prefixOfUserDir(want.UserDir())
	require.True(t, ok)
	assert.Equal(t, want, got)
	_, ok = prefixOfUserDir("/home/me")
	assert.False(t, ok, "not a drive_c/users/<user> path")
}

func TestPrefixBaseOf_IgnoresTheFoldersSpelling(t *testing.T) {
	base, ok := prefixBaseOf("/pfx/drive_c/users/steamuser/AppData/local/G/mods/a.pak", "AppData/Local/G/Mods/a.pak")
	require.True(t, ok)
	assert.Equal(t, "/pfx/drive_c/users/steamuser", base)
}

// A deploy into a prefix that does not resolve is refused, typed; one with
// no prefix file in it is not.
func TestRefusePrefixItems(t *testing.T) {
	g := &domain.Game{ID: "g"}
	assert.NoError(t, refusePrefixItems(g, []deployItem{{path: domain.ModPathFile("a")}}))
	var refused *PrefixUnresolvedError
	assert.ErrorAs(t, refusePrefixItems(g, []deployItem{{path: domain.PrefixFile("AppData/a.pak")}}), &refused)
	g.PrefixRoot = &domain.ResolvedPrefix{Path: "/pfx", User: "u"}
	assert.NoError(t, refusePrefixItems(g, []deployItem{{path: domain.PrefixFile("AppData/a.pak")}}))
}

// The refusal names the remedy once: the Problem's own when it has one.
func TestPrefixUnresolvedError_NamesTheRemedyOnce(t *testing.T) {
	e := &PrefixUnresolvedError{GameID: "bg3", Problem: "x (`lmm game edit bg3 --prefix <dir>`)"}
	assert.Equal(t, 1, strings.Count(e.Error(), "lmm game edit"))
	e.Problem = "nothing found"
	assert.Contains(t, e.Error(), "`lmm game edit bg3 --prefix <dir>`")
}
