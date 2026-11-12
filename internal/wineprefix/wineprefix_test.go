package wineprefix_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/wineprefix"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePrefix builds a minimal Wine prefix at dir - system.reg and drive_c -
// with a users directory per name, and returns dir.
func makePrefix(t *testing.T, dir string, users ...string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "drive_c", "users"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
	for _, u := range users {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "drive_c", "users", u), 0o755))
	}
	return dir
}

// makeCompatdata builds <lib>/steamapps/compatdata/<appID>/pfx as Proton
// does - a version file beside pfx - stamped modified at mod, and returns
// the pfx path.
func makeCompatdata(t *testing.T, lib, appID string, mod time.Time, users ...string) string {
	t.Helper()
	pfx := makePrefix(t, filepath.Join(lib, "steamapps", "compatdata", appID, "pfx"), users...)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(pfx), "version"), []byte("9.0-4\n"), 0o644))
	for _, p := range []string{filepath.Join(pfx, "system.reg"), pfx} {
		require.NoError(t, os.Chtimes(p, mod, mod))
	}
	return pfx
}

func TestIsPrefix(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, wineprefix.IsPrefix(dir), "an empty directory is not a prefix")
	assert.False(t, wineprefix.IsPrefix(filepath.Join(dir, "missing")))
	makePrefix(t, dir)
	assert.True(t, wineprefix.IsPrefix(dir))
}

func TestProtonVersion(t *testing.T) {
	base := t.TempDir()

	wine := makePrefix(t, filepath.Join(base, "wine"))
	_, ok := wineprefix.ProtonVersion(wine)
	assert.False(t, ok, "a plain Wine prefix has no Proton version file")

	steam := makeCompatdata(t, filepath.Join(base, "lib"), "489830", time.Now())
	v, ok := wineprefix.ProtonVersion(steam)
	assert.True(t, ok)
	assert.Equal(t, "9.0-4", v)

	// umu: the prefix holds the version file and a pfx -> . link.
	umu := makePrefix(t, filepath.Join(base, "umu"))
	require.NoError(t, os.Symlink(".", filepath.Join(umu, "pfx")))
	require.NoError(t, os.WriteFile(filepath.Join(umu, "version"), []byte("GE-Proton9-20\n"), 0o644))
	v, ok = wineprefix.ProtonVersion(umu)
	assert.True(t, ok)
	assert.Equal(t, "GE-Proton9-20", v)
}

func TestSteamCompatdata(t *testing.T) {
	base := t.TempDir()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	t.Run("none", func(t *testing.T) {
		lib := filepath.Join(base, "none")
		require.NoError(t, os.MkdirAll(filepath.Join(lib, "steamapps"), 0o755))
		assert.Empty(t, wineprefix.SteamCompatdata([]string{lib}, "489830"))
	})

	t.Run("one library of several", func(t *testing.T) {
		a, b := filepath.Join(base, "one-a"), filepath.Join(base, "one-b")
		require.NoError(t, os.MkdirAll(filepath.Join(a, "steamapps"), 0o755))
		pfx := makeCompatdata(t, b, "489830", older)
		got := wineprefix.SteamCompatdata([]string{a, b}, "489830")
		require.Len(t, got, 1)
		assert.Equal(t, b, got[0].Library)
		assert.Equal(t, pfx, got[0].Path)
	})

	t.Run("several: newest first, whatever the library order", func(t *testing.T) {
		a, b := filepath.Join(base, "sev-a"), filepath.Join(base, "sev-b")
		makeCompatdata(t, a, "489830", older)
		pfxB := makeCompatdata(t, b, "489830", newer)
		got := wineprefix.SteamCompatdata([]string{a, b}, "489830")
		require.Len(t, got, 2)
		assert.Equal(t, pfxB, got[0].Path)
		assert.Equal(t, a, got[1].Library)
		assert.True(t, got[0].ModTime.Equal(newer), "ModTime is the newest stamp found: %v", got[0].ModTime)
	})

	t.Run("a user.reg written later counts", func(t *testing.T) {
		a, b := filepath.Join(base, "reg-a"), filepath.Join(base, "reg-b")
		pfxA := makeCompatdata(t, a, "489830", older)
		makeCompatdata(t, b, "489830", newer)
		later := newer.Add(time.Hour)
		reg := filepath.Join(pfxA, "user.reg")
		require.NoError(t, os.WriteFile(reg, nil, 0o644))
		require.NoError(t, os.Chtimes(reg, later, later))
		got := wineprefix.SteamCompatdata([]string{a, b}, "489830")
		require.Len(t, got, 2)
		assert.Equal(t, pfxA, got[0].Path)
	})

	t.Run("a pfx that is not a prefix is passed over", func(t *testing.T) {
		lib := filepath.Join(base, "half")
		require.NoError(t, os.MkdirAll(filepath.Join(lib, "steamapps", "compatdata", "489830", "pfx"), 0o755))
		assert.Empty(t, wineprefix.SteamCompatdata([]string{lib}, "489830"))
	})

	t.Run("a tie keeps the libraries' order", func(t *testing.T) {
		a, b, c := filepath.Join(base, "tie-a"), filepath.Join(base, "tie-b"), filepath.Join(base, "tie-c")
		pfxA := makeCompatdata(t, a, "489830", older)
		pfxB := makeCompatdata(t, b, "489830", older)
		pfxC := makeCompatdata(t, c, "489830", older)
		for _, order := range [][]string{{a, b, c}, {c, a, b}} {
			got := wineprefix.SteamCompatdata(order, "489830")
			require.Len(t, got, 3)
			want := map[string]string{a: pfxA, b: pfxB, c: pfxC}
			for i, lib := range order {
				assert.Equal(t, want[lib], got[i].Path, "order %v, position %d", order, i)
			}
		}
	})

	t.Run("the same library twice counts once", func(t *testing.T) {
		lib := filepath.Join(base, "dup")
		makeCompatdata(t, lib, "489830", older)
		link := filepath.Join(base, "dup-link")
		require.NoError(t, os.Symlink(lib, link))
		assert.Len(t, wineprefix.SteamCompatdata([]string{lib, link, lib}, "489830"), 1)
	})

	t.Run("an app id that is not digits names nothing", func(t *testing.T) {
		lib := filepath.Join(base, "bad")
		makeCompatdata(t, lib, "489830", older)
		assert.Empty(t, wineprefix.SteamCompatdata([]string{lib}, "../compatdata/489830"))
		assert.Empty(t, wineprefix.SteamCompatdata([]string{lib}, ""))
	})
}

func TestFindUser(t *testing.T) {
	base := t.TempDir()

	t.Run("steamuser under Proton", func(t *testing.T) {
		pfx := makeCompatdata(t, filepath.Join(base, "proton"), "1", time.Now(), "steamuser", "Public")
		got := wineprefix.FindUser(pfx)
		assert.Equal(t, wineprefix.User{Name: "steamuser", Rule: wineprefix.UserRuleProton}, got)
	})

	t.Run("steamuser under Proton, even beside another user", func(t *testing.T) {
		pfx := makeCompatdata(t, filepath.Join(base, "proton2"), "1", time.Now(), "steamuser", "dyoung", "Public")
		assert.Equal(t, "steamuser", wineprefix.FindUser(pfx).Name)
	})

	t.Run("a single other user", func(t *testing.T) {
		pfx := makePrefix(t, filepath.Join(base, "single"), "dyoung", "Public")
		got := wineprefix.FindUser(pfx)
		assert.Equal(t, wineprefix.User{Name: "dyoung", Rule: wineprefix.UserRuleOnly}, got)
	})

	t.Run("steamuser in a plain Wine prefix is just the only user", func(t *testing.T) {
		pfx := makePrefix(t, filepath.Join(base, "wine-steamuser"), "steamuser", "Public")
		assert.Equal(t, wineprefix.User{Name: "steamuser", Rule: wineprefix.UserRuleOnly}, wineprefix.FindUser(pfx))
	})

	t.Run("Public is ignored, in any case", func(t *testing.T) {
		pfx := makePrefix(t, filepath.Join(base, "public"), "public", "Public")
		got := wineprefix.FindUser(pfx)
		assert.Empty(t, got.Name)
		assert.Empty(t, got.Candidates)
	})

	t.Run("several users", func(t *testing.T) {
		pfx := makePrefix(t, filepath.Join(base, "several"), "alice", "bob", "Public")
		got := wineprefix.FindUser(pfx)
		assert.Empty(t, got.Name)
		assert.Equal(t, []string{"alice", "bob"}, got.Candidates)
	})

	t.Run("files under users are not users", func(t *testing.T) {
		pfx := makePrefix(t, filepath.Join(base, "files"), "alice")
		require.NoError(t, os.WriteFile(filepath.Join(pfx, "drive_c", "users", "desktop.ini"), nil, 0o644))
		assert.Equal(t, "alice", wineprefix.FindUser(pfx).Name)
	})

	t.Run("no users directory", func(t *testing.T) {
		got := wineprefix.FindUser(filepath.Join(base, "missing"))
		assert.Empty(t, got.Name)
		assert.Empty(t, got.Candidates)
	})
}

func TestCheckUserName(t *testing.T) {
	for _, ok := range []string{"steamuser", "dyoung", "John Smith"} {
		assert.NoError(t, wineprefix.CheckUserName(ok), ok)
	}
	for bad, says := range map[string]string{
		"":         "empty",
		".":        "not a directory name",
		"..":       "not a directory name",
		"a/b":      "slash",
		`a\b`:      "slash",
		" ":        "whitespace",
		" dyoung":  "leading or trailing whitespace",
		"dyoung\t": "leading or trailing whitespace",
	} {
		err := wineprefix.CheckUserName(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), says, "%q", bad)
	}
}

// #605 review: a user directory that is a symlink is followed only while it
// stays inside the prefix's drive_c/users - a link to the host's home
// would point a writer outside the prefix.
func TestCheckUserDir(t *testing.T) {
	base := t.TempDir()
	pfx := makePrefix(t, filepath.Join(base, "pfx"), "dyoung", "real")
	users := filepath.Join(pfx, "drive_c", "users")
	outside := filepath.Join(base, "home")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(users, "escape")))
	require.NoError(t, os.Symlink("real", filepath.Join(users, "inside")))
	require.NoError(t, os.Symlink(".", filepath.Join(users, "self")))
	require.NoError(t, os.WriteFile(filepath.Join(users, "file"), nil, 0o644))

	assert.NoError(t, wineprefix.CheckUserDir(pfx, "dyoung"))
	assert.NoError(t, wineprefix.CheckUserDir(pfx, "inside"), "a link that stays inside drive_c/users is fine")
	assert.ErrorIs(t, wineprefix.CheckUserDir(pfx, "escape"), wineprefix.ErrUserOutsidePrefix)
	assert.ErrorIs(t, wineprefix.CheckUserDir(pfx, "self"), wineprefix.ErrUserOutsidePrefix, "drive_c/users itself is not a user directory")
	assert.ErrorIs(t, wineprefix.CheckUserDir(pfx, "file"), wineprefix.ErrNoUserDir)
	assert.ErrorIs(t, wineprefix.CheckUserDir(pfx, "missing"), wineprefix.ErrNoUserDir)

	// drive_c/users itself linked out of the prefix escapes too.
	other := makePrefix(t, filepath.Join(base, "other"))
	require.NoError(t, os.RemoveAll(filepath.Join(other, "drive_c", "users")))
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "u"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(other, "drive_c", "users")))
	assert.ErrorIs(t, wineprefix.CheckUserDir(other, "u"), wineprefix.ErrUserOutsidePrefix)

	// A prefix reached through a symlink is checked against where it is.
	link := filepath.Join(base, "pfx-link")
	require.NoError(t, os.Symlink(pfx, link))
	assert.NoError(t, wineprefix.CheckUserDir(link, "dyoung"))
}

// FindUser applies the same rule to the directory it picks: one that
// leaves the prefix is reported as Outside, never picked.
func TestFindUser_RefusesAUserDirectoryOutsideThePrefix(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "home")
	require.NoError(t, os.MkdirAll(outside, 0o755))

	single := makePrefix(t, filepath.Join(base, "single"), "Public")
	require.NoError(t, os.Symlink(outside, filepath.Join(single, "drive_c", "users", "dyoung")))
	got := wineprefix.FindUser(single)
	assert.Empty(t, got.Name)
	assert.Equal(t, "dyoung", got.Outside)

	proton := makeCompatdata(t, filepath.Join(base, "lib"), "1", time.Now(), "Public")
	require.NoError(t, os.Symlink(outside, filepath.Join(proton, "drive_c", "users", "steamuser")))
	got = wineprefix.FindUser(proton)
	assert.Empty(t, got.Name)
	assert.Equal(t, "steamuser", got.Outside)

	// A link that stays under drive_c/users is followed: here into a
	// folder under Public, which is still inside the prefix.
	inside := makePrefix(t, filepath.Join(base, "inside"), "Public")
	require.NoError(t, os.MkdirAll(filepath.Join(inside, "drive_c", "users", "Public", "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("Public", "real"), filepath.Join(inside, "drive_c", "users", "dyoung")))
	got = wineprefix.FindUser(inside)
	assert.Equal(t, "dyoung", got.Name)
	assert.Empty(t, got.Outside)
}
