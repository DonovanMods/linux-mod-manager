package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDoUpdateRecompileTest builds a DeployCompile game with a registered
// compiler-capable source and an installed, deployed compiled mod whose
// recorded base-pak fingerprint is deliberately wrong, so `lmm update`
// reports/applies a #196 recompile row end to end through the CLI.
// linkMethod is LinkCopy so a successful recompile+redeploy is provable
// from the on-disk deployed bytes (a symlink would trivially reflect an
// in-place cache swap on its own).
func setupDoUpdateRecompileTest(t *testing.T) (*core.Service, *domain.Game, *compilerInstallSource, string) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	oldSource, oldProfile, oldAll, oldDryRun, oldForce := updateSource, updateProfile, updateAll, updateDryRun, updateForce
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	updateSource = "fake-compiler"
	updateProfile = ""
	updateAll = false
	updateDryRun = false
	updateForce = false
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		updateSource, updateProfile, updateAll, updateDryRun, updateForce = oldSource, oldProfile, oldAll, oldDryRun, oldForce
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, "Bear_Mount_P.pak", []byte("stale-compiled-bytes")))
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("retained-exmodz-bytes")))
	versionDir := gameCache.ModPath(game.ID, "fake-compiler", modID, version)
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{"Bear_Mount_P.pak"}))
	require.NoError(t, cache.MarkBaseIndexHash(versionDir, fileID, "0000000000000000000000000000000000dead"))

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   domain.LinkCopy,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(im))
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &im.Mod, "default"))

	pm := svc.NewProfileManager()
	_, cerr := pm.Create(game.ID, "default")
	require.NoError(t, cerr)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	return svc, game, compiler, filepath.Join(game.ModPath, "Bear_Mount_P.pak")
}

// TestDoUpdate_JSON_ReportsRecompileNeeded proves the bulk --json contract
// gained the additive recompile_needed/reason fields (#196) without
// disturbing the rest of the row shape.
func TestDoUpdate_JSON_ReportsRecompileNeeded(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := doUpdate(context.Background(), svc, game, nil)
	w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	var out updateJSONOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	require.Len(t, out.Updates, 1)
	row := out.Updates[0]
	assert.Equal(t, "bear-mount", row.ModID)
	assert.Equal(t, "3.3", row.Current)
	assert.Equal(t, "3.3", row.Available, "a staleness row's available version equals current - the mod hasn't changed")
	assert.True(t, row.RecompileNeeded)
	assert.Equal(t, "stale_compile", row.Reason)
}

// TestApplySingleUpdate_Recompile_AppliesAndRedeploys drives `lmm update
// bear-mount` end to end: the stale compile is recompiled from its retained
// source and redeployed, proven via the on-disk (LinkCopy) deployed bytes.
func TestApplySingleUpdate_Recompile_AppliesAndRedeploys(t *testing.T) {
	svc, game, compiler, deployedPath := setupDoUpdateRecompileTest(t)

	mod, err := svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	err = applySingleUpdate(context.Background(), svc, game, mod, "default")
	require.NoError(t, err)
	assert.Equal(t, 1, compiler.compileCalls)

	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "retained-exmodz-bytes", string(data), "redeploy must reflect the freshly recompiled bytes")
}

// TestApplySingleUpdate_Recompile_JSON proves the single-mod --json contract
// reports "recompiled" for an applied staleness row, with ToVersion equal
// to FromVersion.
func TestApplySingleUpdate_Recompile_JSON(t *testing.T) {
	svc, game, _, _ := setupDoUpdateRecompileTest(t)
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	mod, err := svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err = applySingleUpdate(context.Background(), svc, game, mod, "default")
	w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	_, _ = buf.ReadFrom(r)

	var out singleUpdateJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "recompiled", out.Status)
	assert.Equal(t, "3.3", out.FromVersion)
	assert.Equal(t, "3.3", out.ToVersion)
}

// TestApplySingleUpdate_Recompile_LockedRefuses proves the CLI's locked-
// refusal wording fires for a staleness row too, and never touches the
// locked mod's deployed files.
func TestApplySingleUpdate_Recompile_LockedRefuses(t *testing.T) {
	svc, game, compiler, deployedPath := setupDoUpdateRecompileTest(t)

	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))

	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)

	mod, err := svc.GetInstalledMod("fake-compiler", "bear-mount", "icarus", "default")
	require.NoError(t, err)

	err = applySingleUpdate(context.Background(), svc, game, mod, "default")
	require.NoError(t, err, "a locked skip is reported, not returned as an error")
	assert.Equal(t, 0, compiler.compileCalls, "a locked mod must never be recompiled")

	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a locked mod's deployed files must never be touched")
}
