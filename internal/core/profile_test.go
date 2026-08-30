package core_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileManager_Create(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	profile, err := pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", profile.Name)
	assert.Equal(t, "skyrim-se", profile.GameID)
}

func TestProfileManager_Create_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	assert.Error(t, err) // Should fail - duplicate name
}

// TestProfileManager_CreateOrResetDefault_CreatesWhenAbsent pins the "no
// existing profile" leg: a fresh IsDefault profile named "default" with no
// mods, same as pm.Create would produce for a brand-new game.
func TestProfileManager_CreateOrResetDefault_CreatesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	pm := core.NewProfileManager(dir, database)

	profile, err := pm.CreateOrResetDefault(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "default", profile.Name)
	assert.Equal(t, "skyrim-se", profile.GameID)
	assert.True(t, profile.IsDefault)
	assert.Empty(t, profile.Mods)

	saved, err := pm.Get(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, saved.IsDefault)
}

// TestProfileManager_CreateOrResetDefault_ResetsExisting pins v2 Phase 2
// Task 21's documented overwrite semantics: 'lmm game add' and 'lmm game
// detect' repair have always unconditionally replaced an existing default
// profile's mod list (config.SaveProfile has no existence check). Unlike
// pm.Create, which errors on an already-existing profile name, this
// silently resets it - a game with mods already recorded in "default"
// loses them.
func TestProfileManager_CreateOrResetDefault_ResetsExisting(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	pm := core.NewProfileManager(dir, database)

	_, err = pm.CreateOrResetDefault(context.Background(), "skyrim-se")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(context.Background(), "skyrim-se", "default", domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.0"}))

	before, err := pm.Get(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.NotEmpty(t, before.Mods, "test setup: profile must have a mod before the reset")

	profile, err := pm.CreateOrResetDefault(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods)
	assert.True(t, profile.IsDefault)

	after, err := pm.Get(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	assert.Empty(t, after.Mods, "resetting an existing default profile must wipe its mod list")
}

func TestProfileManager_Create_RejectsPathTraversalName(t *testing.T) {
	// Each payload is a func of the test's temp root so path-shaped payloads
	// stay inside it — even a guard regression can only touch the sandbox.
	fixed := func(name string) func(string) string {
		return func(string) string { return name }
	}
	tests := map[string]func(root string) string{
		"parent traversal": fixed("../evil"),
		"deep traversal":   fixed("../../../etc/cron.d/evil"),
		"subdirectory":     fixed("a/b"),
		"absolute path": func(root string) string {
			return filepath.Join(root, "outside", "evil")
		},
		"empty":           fixed(""),
		"whitespace only": fixed("   "),
	}

	for label, makeName := range tests {
		t.Run(label, func(t *testing.T) {
			// Nest configDir so traversal payloads land inside the walkable
			// temp root instead of escaping it.
			tempDir := t.TempDir()
			configDir := filepath.Join(tempDir, "deep", "nested", "config")
			require.NoError(t, os.MkdirAll(configDir, 0755))
			database, err := db.New(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, database.Close())
			})

			pm := core.NewProfileManager(configDir, database)

			_, err = pm.Create(context.Background(), "skyrim-se", makeName(tempDir))
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
			// The validation error is the user-facing message; it must not be
			// buried under the existence-check wrapping.
			assert.NotContains(t, err.Error(), "checking profile")

			// No file may be written anywhere - inside or outside the
			// profiles directory.
			var files []string
			err = filepath.Walk(tempDir, func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.Mode().IsRegular() {
					files = append(files, path)
				}
				return nil
			})
			require.NoError(t, err)
			assert.Empty(t, files, "no file may be written for an invalid profile name")
		})
	}
}

func TestProfileManager_Import_RejectsPathTraversalGameID(t *testing.T) {
	// The attack vector Copilot flagged on PR #72: profile import parses
	// game_id from untrusted YAML and joins it into the save path.
	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, "deep", "nested", "config")
	require.NoError(t, os.MkdirAll(configDir, 0755))
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(configDir, database)

	_, err = pm.Import(context.Background(), []byte("name: innocent\ngame_id: ../../../evil\nmods: []\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidGameID)

	var files []string
	err = filepath.Walk(tempDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, files, "no file may be written for an invalid game ID")
}

func TestProfileManager_Delete_RejectsPathTraversalName(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	err = pm.Delete(context.Background(), "skyrim-se", "../evil")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidProfileName)
}

func TestProfileManager_List(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)
	_, err = pm.Create(context.Background(), "skyrim-se", "combat")
	require.NoError(t, err)

	profiles, err := pm.List(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.Len(t, profiles, 2)
}

// TestProfileManager_ListNames pins the bare-names query cmd/lmm's
// 'list --profiles' needs (v2 Phase 2 Task 22): unlike List, which loads
// every profile and therefore silently drops one whose YAML fails to
// parse, ListNames only enumerates the profiles directory - the same
// filenames List's own directory scan sees, before any per-file
// config.LoadProfile call.
func TestProfileManager_ListNames(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)
	_, err = pm.Create(context.Background(), "skyrim-se", "combat")
	require.NoError(t, err)

	names, err := pm.ListNames(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"survival", "combat"}, names)
}

// TestProfileManager_ListNames_SurvivesUnparseableProfile is the case List
// can't handle: a profile file present on disk but not valid profile YAML
// still has its bare name returned by ListNames, where List silently skips
// it (List's own loop: `if err != nil { continue }`).
func TestProfileManager_ListNames_SurvivesUnparseableProfile(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)
	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	profileDir := filepath.Join(dir, "games", "skyrim-se", "profiles")
	require.NoError(t, os.WriteFile(filepath.Join(profileDir, "broken.yaml"), []byte("link_method: bogus\n"), 0644))

	names, err := pm.ListNames(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"survival", "broken"}, names)

	profiles, err := pm.List(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.Len(t, profiles, 1, "List silently drops the unparseable profile - the behavior ListNames exists to avoid")
}

func TestProfileManager_Get(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	profile, err := pm.Get(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)
	assert.Equal(t, "survival", profile.Name)
}

func TestProfileManager_Get_NotFound(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Get(context.Background(), "skyrim-se", "nonexistent")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

// TestProfileManager_Get_HonoursCancellation pins v2 Phase 3 Task 18's
// ctx.Err() guard: gameID/name doesn't exist under dir, so an actual disk
// read would fail with domain.ErrProfileNotFound (TestProfileManager_Get_
// NotFound's own scenario, above) - getting context.Canceled instead proves
// the guard returned before Get ever touched disk.
func TestProfileManager_Get_HonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = pm.Get(ctx, "skyrim-se", "nonexistent")
	require.ErrorIs(t, err, context.Canceled)
}

func TestProfileManager_Delete(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	err = pm.Delete(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	_, err = pm.Get(context.Background(), "skyrim-se", "survival")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

func TestProfileManager_SetDefault(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "profile1")
	require.NoError(t, err)
	_, err = pm.Create(context.Background(), "skyrim-se", "profile2")
	require.NoError(t, err)

	err = pm.SetDefault(context.Background(), "skyrim-se", "profile2")
	require.NoError(t, err)

	defaultProfile, err := pm.GetDefault(context.Background(), "skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "profile2", defaultProfile.Name)
}

func TestProfileManager_AddMod(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
	}

	err = pm.AddMod(context.Background(), "skyrim-se", "survival", modRef)
	require.NoError(t, err)

	profile, err := pm.Get(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "12345", profile.Mods[0].ModID)
}

func TestProfileManager_RemoveMod(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)

	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
	}
	err = pm.AddMod(context.Background(), "skyrim-se", "survival", modRef)
	require.NoError(t, err)

	err = pm.RemoveMod(context.Background(), "skyrim-se", "survival", "nexusmods", "12345")
	require.NoError(t, err)

	profile, err := pm.Get(context.Background(), "skyrim-se", "survival")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods)
}

func TestProfileManager_ExportImport(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	// Create a profile with mods
	_, err = pm.Create(context.Background(), "skyrim-se", "original")
	require.NoError(t, err)

	err = pm.AddMod(context.Background(), "skyrim-se", "original", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "123",
		Version:  "1.0",
	})
	require.NoError(t, err)

	// Export it
	data, err := pm.Export(context.Background(), "skyrim-se", "original")
	require.NoError(t, err)
	assert.Contains(t, string(data), "original")
	assert.Contains(t, string(data), "123")

	// Delete the original
	err = pm.Delete(context.Background(), "skyrim-se", "original")
	require.NoError(t, err)

	// Import it back
	imported, err := pm.Import(context.Background(), data)
	require.NoError(t, err)
	assert.Equal(t, "original", imported.Name)
	assert.Len(t, imported.Mods, 1)

	// Verify it exists
	profile, err := pm.Get(context.Background(), "skyrim-se", "original")
	require.NoError(t, err)
	assert.Equal(t, "original", profile.Name)
}

func TestProfileManager_UpsertMod(t *testing.T) {
	dir := t.TempDir()

	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	// Create a profile
	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	// Upsert a new mod (should add it)
	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		FileIDs:  []string{"100"},
	}
	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", modRef)
	require.NoError(t, err)

	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "12345", profile.Mods[0].ModID)
	assert.Equal(t, "1.0.0", profile.Mods[0].Version)
	assert.Equal(t, []string{"100"}, profile.Mods[0].FileIDs)

	// Upsert the same mod with updated version and FileIDs (should update in place)
	modRef2 := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "2.0.0",
		FileIDs:  []string{"200", "201"},
	}
	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", modRef2)
	require.NoError(t, err)

	profile, err = pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1) // Should still be 1 mod, not 2
	assert.Equal(t, "12345", profile.Mods[0].ModID)
	assert.Equal(t, "2.0.0", profile.Mods[0].Version)
	assert.Equal(t, []string{"200", "201"}, profile.Mods[0].FileIDs)

	// Upsert a different mod (should add it)
	modRef3 := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "67890",
		Version:  "1.0.0",
		FileIDs:  []string{"300"},
	}
	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", modRef3)
	require.NoError(t, err)

	profile, err = pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2) // Now should be 2 mods
	// First mod should still be in position 0
	assert.Equal(t, "12345", profile.Mods[0].ModID)
	assert.Equal(t, "67890", profile.Mods[1].ModID)
}

// TestProfileManager_UpsertMod_PreservesLockedMarker guards that Locked
// survives a legitimate in-place update: a SAME-version upsert (a FileIDs
// refresh / reinstall repair) must succeed, update FileIDs, and preserve the
// marker even when the incoming ref carries the zero value for Locked (as
// every install/update-built ref does). A version MOVE on a locked ref is a
// refusal instead - see
// TestProfileManager_UpsertMod_LockedRefRefusesVersionMove (#143).
func TestProfileManager_UpsertMod_PreservesLockedMarker(t *testing.T) {
	dir := t.TempDir()

	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	// Create a profile and add a locked mod
	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	lockedModRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		FileIDs:  []string{"100"},
		Locked:   true,
	}
	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", lockedModRef)
	require.NoError(t, err)

	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.True(t, profile.Mods[0].Locked, "locked marker should be set")

	// Now upsert the same mod at the SAME version with fresh FileIDs and
	// without the Locked flag (fresh/zero-value ref, as install/update
	// operations build them) - a reinstall/repair, which stays legitimate.
	updatedModRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		FileIDs:  []string{"101"},
		Locked:   false,
	}
	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", updatedModRef)
	require.NoError(t, err)

	profile, err = pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "1.0.0", profile.Mods[0].Version, "version stays at the locked version")
	assert.Equal(t, []string{"101"}, profile.Mods[0].FileIDs, "FileIDs should be refreshed")
	assert.True(t, profile.Mods[0].Locked, "locked marker should be preserved despite zero-value in input")
}

// TestProfileManager_UpsertMod_PreservesHooks is the end-to-end guard for
// #295: config.SaveProfile never serialized profile.Hooks/HooksExplicit, so
// every ProfileManager mutation - UpsertMod here, but the same
// load->mutate->config.SaveProfile shape is shared by SetModLock/
// ClearModLock/RemoveMod/ReorderMods/SetDefault - silently wiped a
// profile's hand-edited hooks: override on its very next write. The
// profile's hooks: block is hand-written directly to disk (mirroring the
// issue's own repro: a user hand-edits <profile>.yaml) rather than going
// through SaveProfile, since seeding it via the very function #295 fixes
// would beg the question.
func TestProfileManager_UpsertMod_PreservesHooks(t *testing.T) {
	dir := t.TempDir()

	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	profilePath := filepath.Join(dir, "games", "skyrim-se", "profiles", "test.yaml")
	profileYAML := `name: test
game_id: skyrim-se
mods: []
hooks:
  install:
    before_all: ""
  uninstall:
    after_all: "/home/user/.config/lmm/hooks/cleanup.sh"
`
	require.NoError(t, os.WriteFile(profilePath, []byte(profileYAML), 0644))

	modRef := domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		FileIDs:  []string{"100"},
	}
	require.NoError(t, pm.UpsertMod(context.Background(), "skyrim-se", "test", modRef))

	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1, "the mutation itself must still have applied")

	assert.Equal(t, "", profile.Hooks.Install.BeforeAll)
	assert.True(t, profile.HooksExplicit.Install.BeforeAll, "explicitly-disabled override must survive the mutation")
	assert.Equal(t, "/home/user/.config/lmm/hooks/cleanup.sh", profile.Hooks.Uninstall.AfterAll)
	assert.True(t, profile.HooksExplicit.Uninstall.AfterAll, "hook override must survive UpsertMod, not be wiped")
}

// TestProfileManager_UpsertMod_LockedRefRefusesVersionMove is #143's core
// guard: UpsertMod on a LOCKED ref with a DIFFERENT Version must refuse with
// an error wrapping core.ErrModLocked and leave the profile untouched - the
// record IS the lock's target, and only explicit lock/unlock may move it.
// Without this guard, `lmm install --version <Y>` on a locked mod silently
// moved the lock target while leaving locked:true.
func TestProfileManager_UpsertMod_LockedRefRefusesVersionMove(t *testing.T) {
	dir := t.TempDir()

	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		FileIDs:  []string{"100"},
	})
	require.NoError(t, err)
	require.NoError(t, pm.SetModLock(context.Background(), "skyrim-se", "test", "nexusmods", "12345", ""))

	err = pm.UpsertMod(context.Background(), "skyrim-se", "test", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "2.0.0",
		FileIDs:  []string{"200"},
	})
	require.Error(t, err, "a locked ref's version must not move via UpsertMod")
	assert.True(t, errors.Is(err, core.ErrModLocked), "the refusal must wrap core.ErrModLocked, got: %v", err)
	assert.Contains(t, err.Error(), "nexusmods:12345", "the refusal must name the mod")
	assert.Contains(t, err.Error(), "1.0.0", "the refusal must name the locked version")
	assert.Contains(t, err.Error(), "test", "the refusal must name the profile")
	assert.Contains(t, err.Error(), "lmm mod lock -s nexusmods -p test 12345",
		"the remedy must carry -s/-p so a copy-paste can never act on the wrong source/profile (the LockedRefRefusalError precedent)")
	assert.Contains(t, err.Error(), "lmm mod unlock -s nexusmods -p test 12345",
		"the unlock remedy must carry -s/-p for the same reason")

	// The profile must NOT have been rewritten.
	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "1.0.0", profile.Mods[0].Version, "the locked version must be untouched")
	assert.Equal(t, []string{"100"}, profile.Mods[0].FileIDs, "FileIDs must be untouched on a refused upsert")
	assert.True(t, profile.Mods[0].Locked, "the lock marker must be untouched")
}

// TestProfileManager_ReorderMods_PreservesLockedMarker guards that Locked survives
// when a caller reorders mods by building refs from the loaded profile.
func TestProfileManager_ReorderMods_PreservesLockedMarker(t *testing.T) {
	dir := t.TempDir()

	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	// Create a profile and add two mods, one locked
	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	err = pm.AddMod(context.Background(), "skyrim-se", "test", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		Locked:   true,
	})
	require.NoError(t, err)

	err = pm.AddMod(context.Background(), "skyrim-se", "test", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "67890",
		Version:  "2.0.0",
		Locked:   false,
	})
	require.NoError(t, err)

	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)

	// Reorder with the mods in reverse order
	// (simulates what a caller does: builds the slice from loaded profile.Mods)
	reorderedMods := []domain.ModReference{
		profile.Mods[1], // modID 67890
		profile.Mods[0], // modID 12345 (still locked)
	}

	err = pm.ReorderMods(context.Background(), "skyrim-se", "test", reorderedMods)
	require.NoError(t, err)

	// Verify the order changed and locked marker survived
	profile, err = pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)
	assert.Equal(t, "67890", profile.Mods[0].ModID, "first mod should be reordered")
	assert.Equal(t, "12345", profile.Mods[1].ModID, "second mod should be reordered")
	assert.False(t, profile.Mods[0].Locked, "first mod (67890) should not be locked")
	assert.True(t, profile.Mods[1].Locked, "second mod (12345) should remain locked")
}

// TestProfileManager_SetModLock covers the lock write itself: Locked flips
// true, and a non-empty version moves ref.Version (the lock's target and
// the installed-version record are the same field, #97).
func TestProfileManager_SetModLock(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	require.NoError(t, pm.AddMod(context.Background(), "skyrim-se", "test", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
	}))

	// version == "" locks at the currently-installed version; Version is untouched.
	require.NoError(t, pm.SetModLock(context.Background(), "skyrim-se", "test", "nexusmods", "12345", ""))

	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.True(t, profile.Mods[0].Locked, "locked marker should be set")
	assert.Equal(t, "1.0.0", profile.Mods[0].Version, "version should be untouched when \"\" is given")

	// A non-empty version moves the lock target.
	require.NoError(t, pm.SetModLock(context.Background(), "skyrim-se", "test", "nexusmods", "12345", "2.0.0"))

	profile, err = pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	assert.True(t, profile.Mods[0].Locked)
	assert.Equal(t, "2.0.0", profile.Mods[0].Version, "a non-empty version moves the lock target")
}

// TestProfileManager_SetModLock_NotInProfile pins the not-found error message.
func TestProfileManager_SetModLock_NotInProfile(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	err = pm.SetModLock(context.Background(), "skyrim-se", "test", "nexusmods", "12345", "")
	require.Error(t, err)
	assert.EqualError(t, err, `mod nexusmods:12345 not found in profile "test"`)
}

// TestProfileManager_ClearModLock covers the unlock write: only the marker
// clears, Version is left exactly as it is (it is the installed-version
// record, not lock-only data, #97).
func TestProfileManager_ClearModLock(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	require.NoError(t, pm.AddMod(context.Background(), "skyrim-se", "test", domain.ModReference{
		SourceID: "nexusmods",
		ModID:    "12345",
		Version:  "1.0.0",
		Locked:   true,
	}))

	require.NoError(t, pm.ClearModLock(context.Background(), "skyrim-se", "test", "nexusmods", "12345"))

	profile, err := pm.Get(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.False(t, profile.Mods[0].Locked, "locked marker should be cleared")
	assert.Equal(t, "1.0.0", profile.Mods[0].Version, "version stays - it is the record, not lock-only data")
}

// TestProfileManager_ClearModLock_NotInProfile pins the not-found error message.
func TestProfileManager_ClearModLock_NotInProfile(t *testing.T) {
	dir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	pm := core.NewProfileManager(dir, database)

	_, err = pm.Create(context.Background(), "skyrim-se", "test")
	require.NoError(t, err)

	err = pm.ClearModLock(context.Background(), "skyrim-se", "test", "nexusmods", "12345")
	require.Error(t, err)
	assert.EqualError(t, err, `mod nexusmods:12345 not found in profile "test"`)
}

// TestProfileManager_Mutators_HonourCancellation is the mutator half of
// TestProfileManager_Get_HonoursCancellation (above), which pins a read only.
// v2 Phase 3 Task 18 gave every I/O method a ctx.Err() pre-check ahead of any
// disk access; for a mutator the observable contract is two-part - the call
// returns context.Canceled, AND the profile file on disk is byte-identical
// afterwards - and only a mutator can demonstrate the second half (task-18
// review, Minor 4).
//
// Every case targets the SEEDED profile, so an ordinary uncancelled call
// would succeed and rewrite the file: the unchanged bytes are evidence the
// guard returned first, not that the call had nothing to do.
func TestProfileManager_Mutators_HonourCancellation(t *testing.T) {
	const gameID, profileName = "skyrim-se", "default"
	seeded := domain.ModReference{SourceID: "src", ModID: "m1", Version: "1.0"}

	cases := []struct {
		name string
		call func(context.Context, *core.ProfileManager) error
	}{
		{"Create", func(ctx context.Context, pm *core.ProfileManager) error {
			_, err := pm.Create(ctx, gameID, "second")
			return err
		}},
		{"AddMod", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.AddMod(ctx, gameID, profileName, domain.ModReference{SourceID: "src", ModID: "m2", Version: "1.0"})
		}},
		{"UpsertMod", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.UpsertMod(ctx, gameID, profileName, domain.ModReference{SourceID: "src", ModID: "m1", Version: "2.0"})
		}},
		{"RemoveMod", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.RemoveMod(ctx, gameID, profileName, seeded.SourceID, seeded.ModID)
		}},
		{"SetModLock", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.SetModLock(ctx, gameID, profileName, seeded.SourceID, seeded.ModID, "1.0")
		}},
		{"ClearModLock", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.ClearModLock(ctx, gameID, profileName, seeded.SourceID, seeded.ModID)
		}},
		{"ReorderMods", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.ReorderMods(ctx, gameID, profileName, []domain.ModReference{seeded})
		}},
		{"Export", func(ctx context.Context, pm *core.ProfileManager) error {
			_, err := pm.Export(ctx, gameID, profileName)
			return err
		}},
		{"ImportWithOptions", func(ctx context.Context, pm *core.ProfileManager) error {
			_, err := pm.ImportWithOptions(ctx, nil, false)
			return err
		}},
		{"SetDefault", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.SetDefault(ctx, gameID, profileName)
		}},
		{"CreateOrResetDefault", func(ctx context.Context, pm *core.ProfileManager) error {
			_, err := pm.CreateOrResetDefault(ctx, gameID)
			return err
		}},
		{"Delete", func(ctx context.Context, pm *core.ProfileManager) error {
			return pm.Delete(ctx, gameID, profileName)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.New(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })

			pm := core.NewProfileManager(dir, database)
			_, err = pm.Create(context.Background(), gameID, profileName)
			require.NoError(t, err)
			require.NoError(t, pm.AddMod(context.Background(), gameID, profileName, seeded))

			path := filepath.Join(dir, "games", gameID, "profiles", profileName+".yaml")
			before, err := os.ReadFile(path)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err = tc.call(ctx, pm)
			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)

			after, err := os.ReadFile(path)
			require.NoError(t, err, "the profile file must still be there")
			assert.Equal(t, before, after, "a cancelled mutator must not touch the profile YAML")
		})
	}
}
