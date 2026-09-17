package core_test

// The #466 fix round 1 findings, each tried the way its reviewer tried it:
// every case below sets up a state in which a removal or a deploy used to
// delete, overwrite or mislabel something, and checks it no longer does.
//
//   - F1: with profiles on different link methods, a purge judged the
//     active profile's own links by another profile's fingerprints, called
//     them the user's, and left them.
//   - D2: a copy deploy wrote THROUGH a user's link at a shipped path,
//     overwriting whatever it pointed at.
//   - D4: a deploy that could not preserve an untracked file replaced it
//     anyway.
//   - D5: two games sharing a directory: purging one and then the other
//     deleted a file the user changed, because the second game's record of
//     it had no fingerprint.
//   - F6/D3: a file kept because it could not be checked came back with no
//     fingerprint, and the next purge deleted it unverified.
//   - D8: a hard link edited in place changed the mod's cached copy too, and
//     the remedy "delete it, then deploy" linked the edit straight back.
//   - D9: a replace that failed to record its new files put the old records
//     back without their fingerprints.

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProfile writes profile name of f's game, listing refs, with an
// explicit link method when method is set.
func (f *legacyFixture) writeProfile(t *testing.T, name string, active bool, method string, refs ...string) {
	t.Helper()
	text := "name: " + name + "\ngame_id: " + f.game.ID + "\n"
	if method != "" {
		text += "link_method: " + method + "\n"
	}
	text += "mods:\n"
	if len(refs) == 0 {
		text += "    []\n"
	}
	for _, ref := range refs {
		text += "    - source_id: local\n      mod_id: " + ref + "\n      version: unknown\n"
	}
	if active {
		text += "is_default: true\n"
	}
	dir := filepath.Join(f.svc.ConfigDir(), "games", f.game.ID, "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(text), 0o644))
}

// cachedOnly seeds modID as installed under profile - its files in the
// cache, nothing deployed and nothing recorded.
func (f *legacyFixture) cachedOnly(t *testing.T, profile, modID string, method domain.LinkMethod, files map[string]string) {
	t.Helper()
	gameCache := f.svc.GetGameCache(f.game)
	for rel, content := range files {
		require.NoError(t, gameCache.Store(f.game.ID, "local", modID, "unknown", rel, []byte(content)))
	}
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "local", Name: "Mod " + modID, Version: "unknown", GameID: f.game.ID},
		ProfileName:  profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		LinkMethod:   method,
	}))
}

// deploy deploys profile and fails the test on an error.
func (f *legacyFixture) deploy(t *testing.T, profile string, opts core.DeployOptions) *core.DeployResult {
	t.Helper()
	result, err := f.svc.DeployProfile(context.Background(), f.game, profile, opts, nil)
	require.NoError(t, err)
	return result
}

// fingerprinted reports, per path, whether profile's record of it has a
// checksum.
func (f *legacyFixture) fingerprinted(t *testing.T, profile string) map[string]bool {
	t.Helper()
	rows, err := f.svc.QueryForTest(context.Background(),
		`SELECT relative_path, checksum IS NOT NULL FROM deployed_files WHERE game_id = ? AND profile_name = ?`, f.game.ID, profile)
	require.NoError(t, err)
	out := make(map[string]bool)
	for _, row := range rows {
		out[row[0]] = row[1] == "1"
	}
	return out
}

// mixedMethodState is F1's state: the game deploys by copy, profile
// default deployed mod k by copy (fingerprinted records), and profile sym,
// which deploys by symlink, is now active - so the files on disk are sym's
// links into the cache, recorded without fingerprints, while default still
// holds its fingerprinted records of the same paths.
func mixedMethodState(t *testing.T) *legacyFixture {
	t.Helper()
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkCopy))
	f.writeProfile(t, "default", true, "", "k")
	f.cachedOnly(t, "default", "k", domain.LinkCopy, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"})
	f.deploy(t, "default", core.DeployOptions{})
	require.Equal(t, map[string]bool{"Data/k.esp": true, "Data/k2.esp": true}, f.fingerprinted(t, "default"))

	f.writeProfile(t, "sym", false, "symlink", "k")
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "sym")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	info, err := os.Lstat(f.kPath())
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "sym deployed a link")
	require.Equal(t, map[string]bool{"Data/k.esp": false, "Data/k2.esp": false}, f.fingerprinted(t, "sym"))
	require.Equal(t, map[string]bool{"Data/k.esp": true, "Data/k2.esp": true}, f.fingerprinted(t, "default"))
	return f
}

func TestDeployedIdentity_MixedMethodProfilesStillRemoveLmmsOwnLinks(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, f *legacyFixture) []string
	}{
		{"purge", func(t *testing.T, f *legacyFixture) []string {
			plan, err := f.svc.PlanPurge(ctx, f.game, "sym", core.PurgeOptions{})
			require.NoError(t, err)
			require.False(t, plan.RecordedOnly)
			result, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			return append(keptTexts(result.Kept), result.Warnings...)
		}},
		{"uninstall", func(t *testing.T, f *legacyFixture) []string {
			result, err := f.svc.UninstallMod(ctx, f.game, "sym", "local", "k", core.UninstallOptions{})
			require.NoError(t, err)
			return result.Warnings
		}},
		{"mod disable", func(t *testing.T, f *legacyFixture) []string {
			result, err := f.svc.DisableMod(ctx, f.game, "sym", "local", "k")
			require.NoError(t, err)
			return result.Warnings
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := mixedMethodState(t)

			messages := tc.run(t, f)

			_, err := os.Lstat(f.kPath())
			assert.ErrorIs(t, err, os.ErrNotExist, "sym's own link goes")
			assert.False(t, containsLine(messages, "left in place"), "%q", messages)
		})
	}

	t.Run("verify", func(t *testing.T) {
		f := mixedMethodState(t)
		report, err := f.svc.VerifyReport(ctx, f.game, "sym", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		for _, fd := range report.Result.Findings {
			assert.NotEqual(t, core.VerifyStatusDeployedModified, fd.Status, "%+v", fd)
		}
	})
}

// userLinkState is D2's state: a copy game whose profile lists mod k,
// installed and not yet deployed, and the user's own link at the path k
// ships, pointing at a file outside the game directory.
func userLinkState(t *testing.T, method domain.LinkMethod) (f *legacyFixture, precious string) {
	t.Helper()
	f = newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
	f.writeProfile(t, "default", true, "", "k")
	f.cachedOnly(t, "default", "k", method, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"})
	precious = filepath.Join(t.TempDir(), "precious.txt")
	require.NoError(t, os.WriteFile(precious, []byte("PRECIOUS"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(f.kPath()), 0o755))
	require.NoError(t, os.Symlink(precious, f.kPath()))
	return f, precious
}

// zipOf writes an archive holding members.
func zipOf(t *testing.T, members map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "K-1.0.zip")
	out, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(out)
	for name, content := range members {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, out.Close())
	return path
}

func TestDeployedIdentity_ADeployNeverWritesThroughAUsersLink(t *testing.T) {
	ctx := context.Background()
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink, domain.LinkSymlink} {
		for _, tc := range []struct {
			name string
			run  func(t *testing.T, f *legacyFixture) []string
		}{
			{"deploy", func(t *testing.T, f *legacyFixture) []string {
				return f.deploy(t, "default", core.DeployOptions{}).Warnings
			}},
			{"mod enable", func(t *testing.T, f *legacyFixture) []string {
				_, err := f.svc.DisableMod(ctx, f.game, "default", "local", "k")
				require.NoError(t, err)
				result, err := f.svc.EnableMod(ctx, f.game, "default", "local", "k")
				require.NoError(t, err)
				return result.Warnings
			}},
			{"update onto a path the new version adds", func(t *testing.T, f *legacyFixture) []string {
				// The deployed version ships only Data/k2.esp; rolling back
				// to one that also ships Data/k.esp is a replace that adds it.
				require.NoError(t, os.Remove(filepath.Join(f.svc.GetGameCache(f.game).ModPath(f.game.ID, "local", "k", "unknown"), "Data", "k.esp")))
				f.deploy(t, "default", core.DeployOptions{})
				require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "k", "old", "Data/k.esp", []byte("old k")))
				require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "local", "k", "old", "Data/k2.esp", []byte("old k2")))
				require.NoError(t, f.svc.ExecForTest(ctx, `UPDATE installed_mods SET previous_version = 'old' WHERE mod_id = 'k'`))
				plan, err := f.svc.PlanRollback(ctx, f.game, "default", "local", "k")
				require.NoError(t, err)
				result, err := f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
				require.NoError(t, err)
				require.Equal(t, core.UpdateRolledBack, result.Status, "%+v", result)
				return result.Warnings
			}},
			{"import", func(t *testing.T, f *legacyFixture) []string {
				archive := zipOf(t, map[string]string{"Data/k.esp": "imported k", "Data/k3.esp": "imported k3"})
				plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, core.ImportArchiveOptions{})
				require.NoError(t, err)
				result, err := f.svc.ApplyImportArchive(ctx, f.game, "default", plan, core.ImportArchiveOptions{}, nil)
				require.NoError(t, err)
				return result.Warnings
			}},
		} {
			t.Run(method.String()+"/"+tc.name, func(t *testing.T) {
				f, precious := userLinkState(t, method)

				messages := tc.run(t, f)

				assert.Equal(t, "PRECIOUS", readLive(t, precious), "the link's target is untouched")
				target, err := os.Readlink(f.kPath())
				require.NoError(t, err, "the user's link is still there")
				assert.Equal(t, precious, target)
				assert.True(t, containsLine(messages, "Data/k.esp was not replaced", "link"), "the flow says so: %q", messages)
			})
		}
	}

	// #466 re-review R1: another profile's record of the path, with no
	// fingerprint, made the user's link lmm's, and the import replaced it.
	t.Run("import over another profile's unfingerprinted record", func(t *testing.T) {
		f, precious := otherProfilesLinkState(t)
		archive := zipOf(t, map[string]string{"Data/k.esp": "imported k", "Data/k3.esp": "imported k3"})
		plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, core.ImportArchiveOptions{})
		require.NoError(t, err)
		result, err := f.svc.ApplyImportArchive(ctx, f.game, "default", plan, core.ImportArchiveOptions{}, nil)
		require.NoError(t, err)

		assert.Equal(t, "PRECIOUS", readLive(t, precious), "the link's target is untouched")
		target, err := os.Readlink(f.kPath())
		require.NoError(t, err, "the user's link is still there")
		assert.Equal(t, precious, target)
		assert.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "could not be preserved", "it is a link"), "%q", result.Warnings)
		assert.Equal(t, 1, result.Deployed, "the archive's other file is the only one written")
	})
}

// otherProfilesLinkState is the #466 re-review R1 state: F1's mixed-method
// profiles, switched back to default, with mod k then uninstalled there -
// so only profile sym's unfingerprinted records of k's paths remain - and
// the user's own link at Data/k.esp, pointing outside the game.
func otherProfilesLinkState(t *testing.T) (f *legacyFixture, precious string) {
	t.Helper()
	ctx := context.Background()
	f = mixedMethodState(t)
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "default")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	_, err = f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{Force: true})
	require.NoError(t, err)
	require.Empty(t, f.fingerprinted(t, "default"))
	require.Equal(t, map[string]bool{"Data/k.esp": false, "Data/k2.esp": false}, f.fingerprinted(t, "sym"))
	_, err = os.Lstat(f.kPath())
	require.ErrorIs(t, err, os.ErrNotExist)

	precious = filepath.Join(t.TempDir(), "precious.txt")
	require.NoError(t, os.WriteFile(precious, []byte("PRECIOUS"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Dir(f.kPath()), 0o755))
	require.NoError(t, os.Symlink(precious, f.kPath()))
	return f, precious
}

// TestDeployedIdentity_ARemovalLeavesAUsersLinkAnotherProfileRecords is
// R1's removal side: an uninstall in a profile with no record of the path
// does not take another profile's unfingerprinted record as proof that the
// user's link is lmm's.
func TestDeployedIdentity_ARemovalLeavesAUsersLinkAnotherProfileRecords(t *testing.T) {
	ctx := context.Background()
	f, precious := otherProfilesLinkState(t)
	f.cachedOnly(t, "default", "k", domain.LinkCopy, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"})

	result, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{Force: true})
	require.NoError(t, err)

	target, err := os.Readlink(f.kPath())
	require.NoError(t, err, "the user's link is still there")
	assert.Equal(t, precious, target)
	assert.Equal(t, "PRECIOUS", readLive(t, precious))
	assert.True(t, containsLine(result.Warnings, "Data/k.esp was left in place", "link"), "%q", result.Warnings)
	assert.Equal(t, map[string]bool{"Data/k.esp": false, "Data/k2.esp": false}, f.fingerprinted(t, "sym"), "sym's records are sym's")
}

func TestDeployedIdentity_ADeployKeepsAnUntrackedFileItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink, domain.LinkSymlink} {
		t.Run(method.String(), func(t *testing.T) {
			f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
			f.writeProfile(t, "default", true, "", "k")
			f.cachedOnly(t, "default", "k", method, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"})
			require.NoError(t, os.MkdirAll(filepath.Dir(f.kPath()), 0o755))
			require.NoError(t, os.WriteFile(f.kPath(), []byte("PRECIOUS"), 0o000))
			t.Cleanup(func() { _ = os.Chmod(f.kPath(), 0o644) })

			result := f.deploy(t, "default", core.DeployOptions{})

			require.NoError(t, os.Chmod(f.kPath(), 0o644))
			info, err := os.Lstat(f.kPath())
			require.NoError(t, err)
			assert.True(t, info.Mode().IsRegular(), "the file is still a file")
			assert.Equal(t, "PRECIOUS", readLive(t, f.kPath()))
			assert.Equal(t, "mod k2", readLive(t, filepath.Join(f.game.ModPath, "Data", "k2.esp")), "the other files still deploy")
			assert.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "could not be preserved"), "%q", result.Warnings)
			assert.Equal(t, []string{"Data/k2.esp"}, f.recorded(t, "default", "k"), "nothing records the path lmm did not write")
		})
	}
}

// sharedDirState is D5's state: games sky and sky2 share one mod
// directory, and each deployed mod k there, sky first.
func sharedDirState(t *testing.T, method domain.LinkMethod) (sky, sky2 *legacyFixture) {
	t.Helper()
	root := t.TempDir()
	sky = newLegacyFixture(t, handoffGame(t, "sky", root, method))
	sky2 = &legacyFixture{svc: sky.svc, game: handoffGame(t, "sky2", root, method)}
	require.NoError(t, sky.svc.SaveGame(context.Background(), sky2.game))
	for _, f := range []*legacyFixture{sky, sky2} {
		f.writeProfile(t, "default", true, "", "k")
		f.cachedOnly(t, "default", "k", method, map[string]string{"Data/k.esp": "mod k", "Data/k2.esp": "mod k2"})
		f.deploy(t, "default", core.DeployOptions{})
	}
	require.Equal(t, map[string]bool{"Data/k.esp": true, "Data/k2.esp": true}, sky2.fingerprinted(t, "default"),
		"sky2 records sky's file with what is there")
	return sky, sky2
}

func TestDeployedIdentity_TwoGamesPurgedInTurnKeepAFileTheUserChanged(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkHardlink} {
		t.Run(method.String(), func(t *testing.T) {
			ctx := context.Background()
			sky, sky2 := sharedDirState(t, method)
			require.NoError(t, os.Remove(sky.kPath()))
			require.NoError(t, os.WriteFile(sky.kPath(), []byte("USER FILE"), 0o644))

			plan, err := sky.svc.PlanPurge(ctx, sky.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			first, err := sky.svc.ApplyPurge(ctx, sky.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
			assert.Contains(t, first.Kept, core.PurgeKeptPath{Path: "Data/k.esp", Reason: core.PurgeKeptUserFile, Note: "its content changed after lmm deployed it"},
				"the first purge says the file is the user's")

			plan, err = sky2.svc.PlanPurge(ctx, sky2.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			second, err := sky2.svc.ApplyPurge(ctx, sky2.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)

			assert.Equal(t, "USER FILE", readLive(t, sky.kPath()))
			assert.Contains(t, second.Kept, core.PurgeKeptPath{Path: "Data/k.esp", Reason: core.PurgeKeptUserFile, Note: "its content changed after lmm deployed it"})
			assert.False(t, containsLine(second.Warnings, "unverified"), "%q", second.Warnings)
			assert.NoFileExists(t, filepath.Join(sky.game.ModPath, "Data", "k2.esp"), "the untouched file goes with the last game")
		})
	}
}

func TestDeployedIdentity_AFileThatCouldNotBeCheckedKeepsItsFingerprint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))
	dir := filepath.Dir(f.kPath())
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	result, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{Purge: true}, nil)
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, err)
	assert.True(t, containsLine(result.Warnings, "Data/k.esp was left in place", "could not be checked"), "%q", result.Warnings)
	assert.Equal(t, map[string]bool{"Data/k.esp": true, "Data/k2.esp": true}, f.fingerprinted(t, "default"),
		"a file that could not be checked keeps its record exactly as it was")

	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	purged, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "USER FILE", readLive(t, f.kPath()), "and the next purge still knows it is the user's")
	assert.False(t, containsLine(purged.Warnings, "unverified"), "%q", purged.Warnings)
}

func TestDeployedIdentity_AnUncheckedFileKeepsItsRecordOnEveryRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, f *legacyFixture)
	}{
		{"purge", func(t *testing.T, f *legacyFixture) {
			plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
			require.NoError(t, err)
			_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
			require.NoError(t, err)
		}},
		{"uninstall", func(t *testing.T, f *legacyFixture) {
			_, err := f.svc.UninstallMod(ctx, f.game, "default", "local", "k", core.UninstallOptions{})
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ledgerState(t, domain.LinkCopy)
			dir := filepath.Dir(f.kPath())
			require.NoError(t, os.Chmod(dir, 0o000))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

			tc.run(t, f)

			require.NoError(t, os.Chmod(dir, 0o755))
			assert.FileExists(t, f.kPath())
			assert.True(t, f.fingerprinted(t, "default")["Data/k.esp"], "the record stays, fingerprint and all")
		})
	}
}

func TestDeployedIdentity_AHardLinkEditedInPlaceNamesAReinstall(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkHardlink)
	// An edit in place: the same inode, which the cache shares.
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER EDIT"), 0o644))

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var reasons []string
	for _, fd := range report.Result.Findings {
		if fd.Status == core.VerifyStatusDeployedModified {
			reasons = append(reasons, fd.FixableReason)
		}
	}
	assert.True(t, containsLine(reasons, "cached copy", "lmm install --force"), "%q", reasons)

	result := f.deploy(t, "default", core.DeployOptions{})
	assert.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "lmm install --force"), "%q", result.Warnings)

	// A replaced file is a new inode: deleting it and redeploying is enough.
	g := ledgerState(t, domain.LinkHardlink)
	require.NoError(t, os.Remove(g.kPath()))
	require.NoError(t, os.WriteFile(g.kPath(), []byte("USER FILE"), 0o644))
	result = g.deploy(t, "default", core.DeployOptions{})
	assert.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "delete it, then deploy again"), "%q", result.Warnings)
}

func TestDeployedIdentity_AFailedReplacePutsBackTheOldRecordsExactly(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	gameCache := f.svc.GetGameCache(f.game)
	require.NoError(t, gameCache.Store(f.game.ID, "local", "k", "old", "Data/k.esp", []byte("old k")))
	require.NoError(t, gameCache.Store(f.game.ID, "local", "k", "old", "Data/k2.esp", []byte("old k2")))
	require.NoError(t, gameCache.Store(f.game.ID, "local", "k", "old", "Data/new.esp", []byte("old new")))
	require.NoError(t, f.svc.ExecForTest(ctx, `UPDATE installed_mods SET previous_version = 'old' WHERE mod_id = 'k'`))
	// Recording the new version's one new path fails.
	require.NoError(t, f.svc.ExecForTest(ctx, `CREATE TRIGGER fail_new BEFORE INSERT ON deployed_files
		WHEN NEW.relative_path = 'Data/new.esp' BEGIN SELECT RAISE(ABORT, 'disk full'); END`))

	plan, err := f.svc.PlanRollback(ctx, f.game, "default", "local", "k")
	require.NoError(t, err)
	_, err = f.svc.ApplyRollback(ctx, f.game, plan, core.RollbackOptions{}, nil)
	require.Error(t, err)

	assert.Equal(t, map[string]bool{"Data/k.esp": true, "Data/k2.esp": true}, f.fingerprinted(t, "default"),
		"the old records are back with their fingerprints")
	assert.Equal(t, "mod k", readLive(t, f.kPath()))
}

// TestDeployedIdentity_TheReviewersScenariosNameEveryKeptPath is a guard on
// the notes themselves: each names its path first.
func TestDeployedIdentity_TheReviewersScenariosNameEveryKeptPath(t *testing.T) {
	f, _ := userLinkState(t, domain.LinkCopy)
	result := f.deploy(t, "default", core.DeployOptions{})
	for _, w := range result.Warnings {
		assert.True(t, strings.HasPrefix(w, "Data/"), "%q", w)
	}
}

// TestDeployedIdentity_AnImportCountsOnlyTheFilesItWrote (#466 review D7,
// #476): `lmm import --force` over a copy the user changed keeps the file,
// says so, and does not count it as deployed.
func TestDeployedIdentity_AnImportCountsOnlyTheFilesItWrote(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkCopy))
	f.writeProfile(t, "default", true, "")
	archive := zipOf(t, map[string]string{"Data/k.esp": "imported k", "Data/k2.esp": "imported k2"})
	importIt := func(opts core.ImportArchiveOptions) *core.ImportArchiveResult {
		t.Helper()
		plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, opts)
		require.NoError(t, err)
		result, err := f.svc.ApplyImportArchive(ctx, f.game, "default", plan, opts, nil)
		require.NoError(t, err)
		return result
	}
	first := importIt(core.ImportArchiveOptions{})
	require.Equal(t, 2, first.Deployed)
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))

	second := importIt(core.ImportArchiveOptions{SourceID: first.Mod.SourceID, ModID: first.Mod.ID, Force: true})

	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
	assert.Equal(t, 1, second.Deployed, "only the file it wrote")
	assert.True(t, containsLine(second.Warnings, "Data/k.esp was not replaced"), "%q", second.Warnings)
}

// TestDeployedIdentity_ADeployThatSetsAKeptFileAsideSaysSo (#466 review
// F5): a purge told the user it kept their file and stopped tracking it;
// the next deploy may set that file aside and put the mod's version there,
// but it says so, and a purge puts the user's file back.
func TestDeployedIdentity_ADeployThatSetsAKeptFileAsideSaysSo(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER EDIT 3"), 0o644))
	plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	purged, err := f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	require.True(t, containsLine(keptTexts(purged.Kept), "user_file Data/k.esp"), "%+v", purged.Kept)

	result := f.deploy(t, "default", core.DeployOptions{})

	assert.Equal(t, "mod k", readLive(t, f.kPath()))
	assert.True(t, containsLine(result.Warnings, "Data/k.esp is the file you changed", "set it aside", "lmm purge"), "%q", result.Warnings)

	// Said once: the next deploy has nothing to set aside.
	result = f.deploy(t, "default", core.DeployOptions{})
	assert.False(t, containsLine(result.Warnings, "Data/k.esp"), "%q", result.Warnings)

	plan, err = f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "USER EDIT 3", readLive(t, f.kPath()), "the purge puts the user's file back")
}

// TestDeployedIdentity_VerifyReportsAPathADeployCouldNotTake (#466 review
// D6): the user changed a deployed file twice across purges, so the second
// time the originals store already held an earlier original and the deploy
// left the path, recording nothing. verify says so instead of "All files
// verified OK".
func TestDeployedIdentity_VerifyReportsAPathADeployCouldNotTake(t *testing.T) {
	ctx := context.Background()
	f := ledgerState(t, domain.LinkCopy)
	purge := func() {
		t.Helper()
		plan, err := f.svc.PlanPurge(ctx, f.game, "default", core.PurgeOptions{})
		require.NoError(t, err)
		_, err = f.svc.ApplyPurge(ctx, f.game, plan, core.PurgeOptions{}, nil)
		require.NoError(t, err)
	}
	require.NoError(t, os.WriteFile(f.kPath(), []byte("U1"), 0o644))
	purge()
	f.deploy(t, "default", core.DeployOptions{}) // sets U1 aside
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("U2"), 0o644)) // lmm's file replaced again
	purge()
	result := f.deploy(t, "default", core.DeployOptions{})
	require.True(t, containsLine(result.Warnings, "Data/k.esp was not replaced", "earlier original"), "%q", result.Warnings)
	require.Equal(t, "U2", readLive(t, f.kPath()))

	report, err := f.svc.VerifyReport(ctx, f.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	var blocked []core.VerifyFinding
	for _, fd := range report.Result.Findings {
		if fd.Status == core.VerifyStatusDeployedBlocked {
			blocked = append(blocked, fd)
		}
	}
	require.Len(t, blocked, 1, "%+v", report.Result.Findings)
	assert.Equal(t, "Data/k.esp", blocked[0].FileID)
	assert.False(t, blocked[0].Fixable)
	assert.Positive(t, report.Result.Warnings)

	// An ordinary deployment has no such row.
	g := ledgerState(t, domain.LinkCopy)
	report, err = g.svc.VerifyReport(ctx, g.game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	for _, fd := range report.Result.Findings {
		assert.NotEqual(t, core.VerifyStatusDeployedBlocked, fd.Status, "%+v", fd)
	}
}

// TestDeployedIdentity_ADeployPurgeReportsAnUncheckedFileOnce is #466
// re-review R5: `deploy --purge` over a file that cannot be checked said
// it was left in place twice - the purge half and the deploy half each
// noted it - in the result's warnings and in the events.
func TestDeployedIdentity_ADeployPurgeReportsAnUncheckedFileOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := ledgerState(t, domain.LinkCopy)
	require.NoError(t, os.Remove(f.kPath()))
	require.NoError(t, os.WriteFile(f.kPath(), []byte("USER FILE"), 0o644))
	dir := filepath.Dir(f.kPath())
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	var events []string
	result, err := f.svc.DeployProfile(context.Background(), f.game, "default", core.DeployOptions{Purge: true}, func(e core.Event) {
		if w, ok := e.(core.WarningEvent); ok {
			events = append(events, w.Message)
		}
	})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))

	assert.Equal(t, "USER FILE", readLive(t, f.kPath()))
	assert.Equal(t, 1, countLines(result.Warnings, "Data/k.esp was left in place", "could not be checked"), "%q", result.Warnings)
	assert.Equal(t, 1, countLines(events, "Data/k.esp was left in place", "could not be checked"), "%q", events)
}

// countLines counts the lines holding every part.
func countLines(lines []string, parts ...string) int {
	n := 0
	for _, line := range lines {
		if containsLine([]string{line}, parts...) {
			n++
		}
	}
	return n
}
