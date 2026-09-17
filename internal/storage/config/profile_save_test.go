package config_test

// #441: SaveProfile wrote to <name:>.yaml rather than the file the profile
// was read from, truncated the file before writing it, and rebuilt the
// document from scratch - dropping comments, expanding `~` and writing
// eight `null` hook keys - on every change.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestSaveProfile_WritesTheFileTheProfileWasLoadedFrom is the issue's
// reproduction: default.yaml copied by hand to vanilla.yaml, `name:` left
// as it was. A change to vanilla overwrote default.yaml - its disabled
// marker and its is_default with it - and left vanilla.yaml as it was.
func TestSaveProfile_WritesTheFileTheProfileWasLoadedFrom(t *testing.T) {
	dir := t.TempDir()
	original := "name: default\ngame_id: g1\nmods:\n    - source_id: src\n      mod_id: a\n      disabled: true\nis_default: true\n"
	defaultPath := writeProfileFile(t, dir, "g1", "default", original)
	vanillaPath := writeProfileFile(t, dir, "g1", "vanilla", original)

	vanilla, err := config.LoadProfile(dir, "g1", "vanilla")
	require.NoError(t, err)
	assert.Equal(t, "vanilla", vanilla.Name, "a profile is named for its file")
	declared, err := config.DeclaredProfileName(dir, "g1", "vanilla")
	require.NoError(t, err)
	assert.Equal(t, "default", declared, "what the file says is still there to report")

	vanilla.IsDefault = false
	vanilla.Mods[0].Disabled = false
	vanilla.Mods = append(vanilla.Mods, domain.ModReference{SourceID: "src", ModID: "b"})
	require.NoError(t, config.SaveProfile(dir, vanilla))

	assert.Equal(t, original, readFile(t, defaultPath), "the file vanilla was copied from is untouched")
	assert.Equal(t, "name: default\ngame_id: g1\nmods:\n    - source_id: src\n      mod_id: a\n    - source_id: src\n      mod_id: b\n",
		readFile(t, vanillaPath), "vanilla's own file changed - and its `name:` was left for the user")
	reloaded, err := config.LoadProfile(dir, "g1", "vanilla")
	require.NoError(t, err)
	assert.Equal(t, vanilla, reloaded)

	// The same goes for a profile copied into another game's directory.
	otherPath := writeProfileFile(t, dir, "g2", "default", original)
	copied, err := config.LoadProfile(dir, "g2", "default")
	require.NoError(t, err)
	assert.Equal(t, "g2", copied.GameID)
	copied.IsDefault = false
	require.NoError(t, config.SaveProfile(dir, copied))
	assert.Equal(t, original, readFile(t, defaultPath), "g1's profile is untouched")
	assert.NotContains(t, readFile(t, otherPath), "is_default")
}

func TestSaveProfile_ReplacesTheFileAtomically(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "g1", "p", "name: p\ngame_id: g1\nmods: []\n")
	require.NoError(t, os.Chmod(path, 0o640))
	before, err := os.Stat(path)
	require.NoError(t, err)

	profile, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)
	profile.IsDefault = true
	require.NoError(t, config.SaveProfile(dir, profile))

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, after), "a new file was renamed over the old one, not the old one truncated")
	assert.Equal(t, fs.FileMode(0o640), after.Mode().Perm(), "and it keeps the file's mode")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file is left behind")
	assert.Equal(t, "name: p\ngame_id: g1\nmods: []\nis_default: true\n", readFile(t, path))

	// A profile kept elsewhere and linked in: the link survives, its
	// target changes.
	target := filepath.Join(t.TempDir(), "p.yaml")
	require.NoError(t, os.WriteFile(target, []byte("name: q\ngame_id: g1\nmods: []\n"), 0o644))
	link, err := config.ProfilePath(dir, "g1", "q")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(target, link))
	linked, err := config.LoadProfile(dir, "g1", "q")
	require.NoError(t, err)
	linked.IsDefault = true
	require.NoError(t, config.SaveProfile(dir, linked))
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&fs.ModeSymlink, "the link is still a link")
	assert.Contains(t, readFile(t, target), "is_default: true")

	if os.Geteuid() != 0 {
		readOnly := writeProfileFile(t, dir, "g1", "ro", "name: ro\ngame_id: g1\nmods: []\n")
		require.NoError(t, os.Chmod(readOnly, 0o444))
		ro, err := config.LoadProfile(dir, "g1", "ro")
		require.NoError(t, err)
		ro.IsDefault = true
		require.ErrorIs(t, config.SaveProfile(dir, ro), fs.ErrPermission)
		assert.Equal(t, "name: ro\ngame_id: g1\nmods: []\n", readFile(t, readOnly))
	}
}

// handEdited is a profile written by hand: comments, a flow-style entry, a
// `~/` hook path, a block-scalar override, two-space indentation, a blank
// line, and its keys in an order of its own.
const handEdited = `# My carefully curated profile
name: default
game_id: tg
is_default: true
hooks:
  install:
    after_all: ~/bin/after.sh   # run my script
mods:
  # Alpha first
  - {source_id: local, mod_id: alpha, version: "1.0"}
  - source_id: local
    mod_id: beta   # the one I care about
    version: "1.0"

  - source_id: local
    mod_id: gamma
overrides:
  Config/Game.ini: |
    [Section]
    key=1
`

// TestSaveProfile_KeepsWhatTheAuthorWrote puts a hand-edited profile through
// every kind of change a save makes, and pins the file after each: only the
// text of what changed moves.
func TestSaveProfile_KeepsWhatTheAuthorWrote(t *testing.T) {
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
		require.Equal(t, profile.Hooks, reloaded.Hooks, name)
	}
	byID := func(p *domain.Profile, id string) *domain.ModReference {
		return p.FindRef("local", id)
	}

	step("lock beta at 2.0", func(p *domain.Profile) {
		byID(p, "beta").Version, byID(p, "beta").Locked = "2.0", true
	}, func(s string) string {
		return strings.Replace(s, "    version: \"1.0\"\n\n", "    version: \"2.0\"\n    locked: true\n\n", 1)
	})

	step("add delta", func(p *domain.Profile) {
		p.Mods = append(p.Mods, domain.ModReference{SourceID: "local", ModID: "delta", Version: "0.1", FileIDs: []string{"d1"}})
	}, func(s string) string {
		return strings.Replace(s, "    mod_id: gamma\n",
			"    mod_id: gamma\n  - source_id: local\n    mod_id: delta\n    version: \"0.1\"\n    file_ids:\n      - d1\n", 1)
	})

	step("give gamma a version and files", func(p *domain.Profile) {
		byID(p, "gamma").Version, byID(p, "gamma").FileIDs = "5", []string{"g1", "g2"}
	}, func(s string) string {
		return strings.Replace(s, "    mod_id: gamma\n",
			"    mod_id: gamma\n    version: \"5\"\n    file_ids:\n      - g1\n      - g2\n", 1)
	})

	step("disable alpha", func(p *domain.Profile) {
		byID(p, "alpha").Disabled = true
	}, func(s string) string {
		return strings.Replace(s, `version: "1.0"}`, `version: "1.0", disabled: true}`, 1)
	})

	step("remove alpha", func(p *domain.Profile) {
		p.Mods = p.Mods[1:]
	}, func(s string) string {
		return strings.Replace(s, "  - {source_id: local, mod_id: alpha, version: \"1.0\", disabled: true}\n", "", 1)
	})

	step("move gamma first", func(p *domain.Profile) {
		p.Mods = []domain.ModReference{p.Mods[1], p.Mods[0], p.Mods[2]}
	}, func(s string) string {
		beta := "  - source_id: local\n    mod_id: beta   # the one I care about\n    version: \"2.0\"\n    locked: true\n"
		gamma := "\n  - source_id: local\n    mod_id: gamma\n    version: \"5\"\n    file_ids:\n      - g1\n      - g2\n"
		return strings.Replace(s, beta+gamma, gamma+beta, 1)
	})

	step("drop gamma's files and version", func(p *domain.Profile) {
		byID(p, "gamma").Version, byID(p, "gamma").FileIDs = "", nil
	}, func(s string) string {
		return strings.Replace(s, "    version: \"5\"\n    file_ids:\n      - g1\n      - g2\n", "", 1)
	})

	step("unlock beta", func(p *domain.Profile) {
		byID(p, "beta").Locked = false
	}, func(s string) string {
		return strings.Replace(s, "    locked: true\n", "", 1)
	})

	step("no longer the default", func(p *domain.Profile) {
		p.IsDefault = false
	}, func(s string) string {
		return strings.Replace(s, "is_default: true\n", "", 1)
	})

	step("an explicit link method", func(p *domain.Profile) {
		p.LinkMethod, p.LinkMethodExplicit = domain.LinkHardlink, true
	}, func(s string) string {
		return strings.Replace(s, "overrides:\n", "link_method: hardlink\noverrides:\n", 1)
	})

	step("the default again", func(p *domain.Profile) {
		p.IsDefault = true
	}, func(s string) string {
		return strings.Replace(s, "link_method: hardlink\n", "link_method: hardlink\nis_default: true\n", 1)
	})

	step("nothing changes", func(*domain.Profile) {}, func(s string) string { return s })

	assert.Contains(t, want, "after_all: ~/bin/after.sh   # run my script", "the hook path is never expanded")
	assert.Contains(t, want, "# My carefully curated profile")
	assert.Contains(t, want, "  Config/Game.ini: |\n    [Section]\n    key=1\n")
}

// TestSaveProfile_WritesNoNullHooks: a whole write used to spell out every
// hook that was not set as `null`.
func TestSaveProfile_WritesNoNullHooks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, config.SaveProfile(dir, &domain.Profile{
		Name: "p", GameID: "g1",
		Hooks:         domain.GameHooks{Install: domain.HookConfig{AfterAll: ""}},
		HooksExplicit: domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}},
	}))
	path, err := config.ProfilePath(dir, "g1", "p")
	require.NoError(t, err)
	assert.Equal(t, "name: p\ngame_id: g1\nmods: []\nhooks:\n    install:\n        after_all: \"\"\n", readFile(t, path))
}

// TestSaveProfile_ALayoutItCannotEditIsRewrittenAndKept: a mods list
// written as one flow sequence cannot be edited entry by entry. The save
// still happens - whole - and the file as the author wrote it is kept
// beside it.
func TestSaveProfile_ALayoutItCannotEditIsRewrittenAndKept(t *testing.T) {
	dir := t.TempDir()
	original := "# mine\nname: p\ngame_id: g1\nmods: [{source_id: s, mod_id: a}]\n"
	path := writeProfileFile(t, dir, "g1", "p", original)

	profile, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)
	profile.Mods = append(profile.Mods, domain.ModReference{SourceID: "s", ModID: "b"})
	require.NoError(t, config.SaveProfile(dir, profile))

	assert.Equal(t, "name: p\ngame_id: g1\nmods:\n    - source_id: s\n      mod_id: a\n    - source_id: s\n      mod_id: b\n", readFile(t, path))
	assert.Equal(t, original, readFile(t, path+".bak"))
	names, err := config.ListProfiles(dir, "g1")
	require.NoError(t, err)
	assert.Equal(t, []string{"p"}, names, "the copy is not a profile")
}

// TestSaveProfile_AnUnchangedProfileIsNotWritten: a save that changes
// nothing leaves the file - and its inode - alone.
func TestSaveProfile_AnUnchangedProfileIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "g1", "p", handEdited)
	before, err := os.Stat(path)
	require.NoError(t, err)

	profile, err := config.LoadProfile(dir, "g1", "p")
	require.NoError(t, err)
	require.NoError(t, config.SaveProfile(dir, profile))

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after))
	assert.Equal(t, handEdited, readFile(t, path))
}

func TestSaveRenamedProfile_KeepsTheDocument(t *testing.T) {
	dir := t.TempDir()
	original := "# the good one\nname: old\ngame_id: g1\nmods:\n  - source_id: s   # keep\n    mod_id: a\n"
	oldPath := writeProfileFile(t, dir, "g1", "old", original)

	profile, err := config.LoadProfile(dir, "g1", "old")
	require.NoError(t, err)
	profile.Name = "new"
	require.NoError(t, config.SaveRenamedProfile(dir, profile, "old"))

	newPath, err := config.ProfilePath(dir, "g1", "new")
	require.NoError(t, err)
	assert.Equal(t, strings.Replace(original, "name: old", "name: new", 1), readFile(t, newPath))
	assert.Equal(t, original, readFile(t, oldPath), "the caller removes the old file")

	// A file whose `name:` never matched keeps it.
	copied := writeProfileFile(t, dir, "g1", "copy", original)
	profile, err = config.LoadProfile(dir, "g1", "copy")
	require.NoError(t, err)
	profile.Name = "renamed"
	require.NoError(t, config.SaveRenamedProfile(dir, profile, "copy"))
	renamedPath, err := config.ProfilePath(dir, "g1", "renamed")
	require.NoError(t, err)
	assert.Equal(t, readFile(t, copied), readFile(t, renamedPath))
}
