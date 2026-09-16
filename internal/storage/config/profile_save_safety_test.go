package config_test

// The #441 review's P2 findings on SaveProfile.
//
//   - F6: the copy a whole rewrite keeps was written with os.WriteFile to
//     <name>.yaml.bak, so a second rewrite replaced the first copy - the
//     author's text gone for good - and a .bak that was a symlink was
//     followed, overwriting whatever it pointed at. A backup is now created
//     exclusively, never through a link and never over a file, at the first
//     free name of .bak, .bak.1, .bak.2, ...
//   - F10: the atomic replace needs a temporary file beside the profile, so a
//     writable profile in a read-only directory stopped saving. It is saved
//     in place there again, and the save says it was not atomic.
//
// CheckProfileSave is F5's precondition: whether a save could be made now,
// decided the way the save decides it, with nothing written.

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowProfile is a layout SaveProfile cannot edit in place (a flow-style
// mods list), so a change to its mods is a whole rewrite with a backup.
const flowProfile = "# mine\nname: p\ngame_id: g1\nmods: [{source_id: s, mod_id: a}]\n"

// addMod loads profile p, appends a reference to id and saves it, returning
// the save's report.
func addMod(t *testing.T, dir, id string) config.SaveReport {
	t.Helper()
	profile, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)
	profile.Mods = append(profile.Mods, domain.ModReference{SourceID: "s", ModID: id})
	report, err := config.SaveProfileReporting(dir, profile)
	require.NoError(t, err)
	return report
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes through permissions")
	}
}

func TestSaveProfile_ABackupNeverReplacesAnother(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "g1", "p", flowProfile)

	first := addMod(t, dir, "b")
	assert.True(t, first.Rewritten)
	assert.Equal(t, path+".bak", first.Backup)
	assert.Equal(t, flowProfile, readFile(t, path+".bak"))
	require.Len(t, first.Notices(), 1)
	assert.Contains(t, first.Notices()[0], path)
	assert.Contains(t, first.Notices()[0], path+".bak")

	// The rewrite left lmm's own layout, which the next save edits in
	// place: no second backup is needed. Put a layout back that needs one.
	second := "# second\nname: p\ngame_id: g1\nmods: [{source_id: s, mod_id: a}]\n"
	require.NoError(t, os.WriteFile(path, []byte(second), 0o644))
	report := addMod(t, dir, "c")
	assert.Equal(t, path+".bak.1", report.Backup)
	assert.Equal(t, flowProfile, readFile(t, path+".bak"), "the first copy is kept")
	assert.Equal(t, second, readFile(t, path+".bak.1"))
	info, err := os.Stat(path + ".bak.1")
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o600), info.Mode().Perm())

	names, err := config.ListProfiles(dir, "g1")
	require.NoError(t, err)
	assert.Equal(t, []string{"p"}, names, "no backup is a profile")
}

func TestSaveProfile_ABackupIsNeverWrittenThroughALink(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "g1", "p", flowProfile)
	important := filepath.Join(t.TempDir(), "important.txt")
	require.NoError(t, os.WriteFile(important, []byte("keep me\n"), 0o644))
	require.NoError(t, os.Symlink(important, path+".bak"))
	dangling := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Symlink(dangling, path+".bak.1"))

	report := addMod(t, dir, "b")
	assert.Equal(t, path+".bak.2", report.Backup)
	assert.Equal(t, "keep me\n", readFile(t, important), "the link's target is untouched")
	assert.NoFileExists(t, dangling, "a dangling link is not followed either")
	target, err := os.Readlink(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, important, target, "the link itself is left alone")
	assert.Equal(t, flowProfile, readFile(t, path+".bak.2"))
}

func TestSaveProfile_AWritableFileInAReadOnlyDirectoryIsSavedInPlace(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "g1", "p", "# mine\nname: p\ngame_id: g1\nmods: []\n")
	profilesDir := filepath.Dir(path)
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(profilesDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(profilesDir, 0o755) })

	profile, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)
	profile.IsDefault = true
	require.NoError(t, config.CheckProfileSave(dir, profile), "the check agrees the save can be made")
	report, err := config.SaveProfileReporting(dir, profile)
	require.NoError(t, err)

	assert.True(t, report.NotAtomic)
	require.Len(t, report.Notices(), 1)
	assert.Contains(t, report.Notices()[0], "not atomically")
	assert.Contains(t, report.Notices()[0], path)
	assert.Equal(t, "# mine\nname: p\ngame_id: g1\nmods: []\nis_default: true\n", readFile(t, path))
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "written in place")
	entries, err := os.ReadDir(profilesDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	t.Run("a save that needs a backup is refused there", func(t *testing.T) {
		require.NoError(t, os.Chmod(profilesDir, 0o755))
		flow := writeProfileFile(t, dir, "g1", "f", flowProfile)
		require.NoError(t, os.Chmod(profilesDir, 0o555))
		profile, err := config.LoadProfile(dir, "g1", "f")
		require.NoError(t, err)
		profile.Mods = append(profile.Mods, domain.ModReference{SourceID: "s", ModID: "b"})

		checked := config.CheckProfileSave(dir, profile)
		require.ErrorIs(t, checked, fs.ErrPermission)
		assert.Contains(t, checked.Error(), flow)
		_, err = config.SaveProfileReporting(dir, profile)
		require.ErrorIs(t, err, fs.ErrPermission)
		assert.Equal(t, flowProfile, readFile(t, flow), "the author's file is untouched")
		assert.NoFileExists(t, flow+".bak")
	})
}

func TestCheckProfileSave(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "g1", "p", "name: p\ngame_id: g1\nmods: []\n")
	profile, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)

	assert.NoError(t, config.CheckProfileSave(dir, profile), "an unchanged profile needs no write")
	profile.IsDefault = true
	assert.NoError(t, config.CheckProfileSave(dir, profile))

	require.NoError(t, os.Chmod(path, 0o444))
	err = config.CheckProfileSave(dir, profile)
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Contains(t, err.Error(), path)
	unchanged, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)
	assert.NoError(t, config.CheckProfileSave(dir, unchanged), "a read-only file that needs no change is fine")
	assert.Equal(t, "name: p\ngame_id: g1\nmods: []\n", readFile(t, path), "checking writes nothing")

	fresh := &domain.Profile{Name: "new", GameID: "g2"}
	assert.NoError(t, config.CheckProfileSave(dir, fresh), "a new profile in a directory lmm can create")
	gamesDir := filepath.Join(dir, "games")
	require.NoError(t, os.Chmod(gamesDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(gamesDir, 0o755) })
	assert.ErrorIs(t, config.CheckProfileSave(dir, fresh), fs.ErrPermission)
	_, err = os.Stat(filepath.Join(gamesDir, "g2"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "checking creates nothing")
}
