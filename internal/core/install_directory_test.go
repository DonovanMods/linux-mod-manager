package core_test

// End-to-end tests for #166: a directory-source mod whose source directory
// SHRINKS between ingests must not keep the removed member in the committed
// cache, in the marker manifest, or deployed in the game directory.
//
// Two flows cover the two re-ingest shapes:
//
//   - a same-version REINSTALL of an installed mod (plan.Replaces set): the
//     reinstall cache transaction downloads into a pristine staging cache and
//     Installer.ReplaceWithOldCache undeploys the members the old snapshot
//     listing no longer serves - the acceptance path for cleaning the game
//     directory.
//   - an install OVER a retained stale cache entry (mod uninstalled with
//     KeepCache, then reinstalled - plan.Replaces nil): the download re-ingests
//     into the EXISTING live cache entry, the seeded-staging shape #166 is
//     about; before the fix the removed member survived the re-ingest and was
//     deployed again.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDirectorySourceInstall builds a Service with a real custom.Directory
// source serving one mod directory (modDirName under its own temp root) and a
// symlink-deploying game, and returns them plus the mod directory path.
func setupDirectorySourceInstall(t *testing.T, modDirName string, memberFiles map[string]string) (*core.Service, *domain.Game, string) {
	t.Helper()

	root := t.TempDir()
	modDir := filepath.Join(root, modDirName)
	require.NoError(t, os.MkdirAll(modDir, 0755))
	for name, content := range memberFiles {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(modDir, name)), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(modDir, name), []byte(content), 0644))
	}

	svc := newFlowsTestService(t)

	src, err := custom.New(custom.SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: root},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "7dtd", Name: "7 Days to Die", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"my-mods": ""},
		DeployMode: domain.DeployExtract,
	}
	return svc, game, modDir
}

// installDirectoryMod plans and applies an install of modID into "default".
func installDirectoryMod(t *testing.T, svc *core.Service, game *domain.Game, modID string) *core.InstallResult {
	t.Helper()
	plan, err := svc.PlanInstall(context.Background(), game, "default", "my-mods", modID, false)
	require.NoError(t, err)
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	return result
}

// TestApplyInstall_DirectorySource_SameVersionReinstall_DropsRemovedMember is
// the #166 end-to-end acceptance for the reinstall repair flow: install a
// directory mod, delete a member from the source, reinstall at the SAME
// version - the removed member must be gone from the committed cache, the
// marker manifest, AND the deployed game directory, and the stored checksum
// must be refreshed (so a subsequent verify run is clean).
func TestApplyInstall_DirectorySource_SameVersionReinstall_DropsRemovedMember(t *testing.T) {
	svc, game, modDir := setupDirectorySourceInstall(t, "BiggerBackpack", map[string]string{
		"ModInfo.xml": `<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`,
		"stale.txt":   "to be removed",
	})

	installDirectoryMod(t, svc, game, "BiggerBackpack")
	require.FileExists(t, filepath.Join(game.ModPath, "stale.txt"), "fixture: the member must be deployed before the source shrinks")

	require.NoError(t, os.Remove(filepath.Join(modDir, "stale.txt")))

	installDirectoryMod(t, svc, game, "BiggerBackpack")

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.NotContains(t, files, "stale.txt", "the removed member must be gone from the committed cache")

	manifests, err := gameCache.FileManifests(game.ID, "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	require.True(t, manifests["main"].Recorded)
	assert.NotContains(t, manifests["main"].Members, "stale.txt", "the removed member must be gone from the marker manifest")

	_, lstatErr := os.Lstat(filepath.Join(game.ModPath, "stale.txt"))
	assert.True(t, os.IsNotExist(lstatErr), "the removed member must be undeployed from the game directory")
	assert.FileExists(t, filepath.Join(game.ModPath, "ModInfo.xml"), "the surviving member must stay deployed")

	checksums, err := svc.GetFilesWithChecksums(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, checksums, 1)
	assert.NotEmpty(t, checksums[0].Checksum, "the reinstall must persist the refreshed digest so verify runs clean")
}

// TestApplyInstall_DirectorySource_InstallOverStaleCacheEntry_DropsRemovedMember
// covers #166's seeded-staging shape directly: the mod is uninstalled with
// KeepCache (the cache entry with the soon-stale member survives), the source
// shrinks, and a fresh install (plan.Replaces nil) re-ingests into that
// EXISTING entry. Before the fix, prepareStaging seeded staging with the old
// entry and copyDir overlaid without deleting, so the removed member stayed in
// the cache and was deployed again.
func TestApplyInstall_DirectorySource_InstallOverStaleCacheEntry_DropsRemovedMember(t *testing.T) {
	svc, game, modDir := setupDirectorySourceInstall(t, "BiggerBackpack", map[string]string{
		"ModInfo.xml": `<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`,
		"stale.txt":   "to be removed",
	})

	installDirectoryMod(t, svc, game, "BiggerBackpack")

	_, err := svc.UninstallMod(context.Background(), game, "default", "my-mods", "BiggerBackpack", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists(game.ID, "my-mods", "BiggerBackpack", "1.2.0"), "fixture: the stale cache entry must survive the uninstall")

	require.NoError(t, os.Remove(filepath.Join(modDir, "stale.txt")))

	installDirectoryMod(t, svc, game, "BiggerBackpack")

	files, err := gameCache.ListFiles(game.ID, "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.NotContains(t, files, "stale.txt", "re-ingest over the retained entry must drop the removed member from the cache")

	_, lstatErr := os.Lstat(filepath.Join(game.ModPath, "stale.txt"))
	assert.True(t, os.IsNotExist(lstatErr), "the removed member must not be deployed to the game directory")
	assert.FileExists(t, filepath.Join(game.ModPath, "ModInfo.xml"))
}

// TestApplyInstall_DirectorySource_ConflictAcceptRerun_AlwaysReingests pins
// the second recorded warm-cache carve-out (task-8 review, Important 1): a
// directory source is deliberately excluded from fillPrimaryCache's
// cache-first guard, so a conflict accept re-run always re-ingests rather
// than reusing what the refused run cached - #166 needs exactly that (the
// source directory can change between the decline and the accept, and only a
// real re-ingest picks up the change; it also costs no network, so skipping
// it would save nothing). Proven the same way #166's own end-to-end tests
// are: change the source's member content BETWEEN the refused run and the
// accept, then assert the DEPLOYED content is the CHANGED one, not the
// refusal's cached snapshot - unlike the fresh/upgrade-install leg
// (DeclineThenAccept_DownloadsExactlyOnce), which would deploy the stale
// snapshot if a source could even change under it the same way.
func TestApplyInstall_DirectorySource_ConflictAcceptRerun_AlwaysReingests(t *testing.T) {
	svc, game, modDir := setupDirectorySourceInstall(t, "newmod", map[string]string{
		"shared.txt": "v1-content",
	})

	seedInstalledMod(t, svc, game, "src", "other", "1.0", true, map[string][]byte{"shared.txt": []byte("original-other-content")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	plan, err := svc.PlanInstall(context.Background(), game, "default", "my-mods", "newmod", false)
	require.NoError(t, err)

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError), "sanity: the fresh ingest is what makes the conflict computable")

	require.NoError(t, os.WriteFile(filepath.Join(modDir, "shared.txt"), []byte("v2-content"), 0644), "simulates the source directory changing between the decline and the accept")

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{AcceptConflicts: true}, nil)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2-content", string(content), "the accept re-run must re-ingest - a directory source is never skipped by the cache-first guard")
}
