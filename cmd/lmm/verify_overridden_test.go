package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoVerify_ReportsAnOverriddenModFile is #630 through the CLI: a deploy
// breaks a mod's link into a copy holding the profile's override, saying
// nothing about it, and `lmm verify` reports the file as overridden by the
// profile - in text and in --json - rather than as drift, with no issue or
// warning counted.
func TestDoVerify_ReportsAnOverriddenModFile(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	game.InstallPath = game.ModPath
	seedDeployableMod(t, svc, game, "1", "Test Mod", "Config/mod.ini")

	pm := svc.NewProfileManager()
	profile, err := pm.Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"Config/mod.ini": []byte("tweaked")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	var deployOut string
	stderr, err := captureStderrErr(t, func() error {
		deployOut = captureStdout(t, func() error {
			return doDeploy(context.Background(), svc, game, nil)
		})
		return nil
	})
	require.NoError(t, err)
	assert.NotContains(t, stderr+deployOut, "override", "a deploy says nothing about an override it applied")

	deployed := filepath.Join(game.ModPath, "Config", "mod.ini")
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the link was broken into a copy")
	content, err := os.ReadFile(deployed)
	require.NoError(t, err)
	assert.Equal(t, "tweaked", string(content))

	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })

	jsonOutput = false
	out := captureStdout(t, func() error { return doVerify(context.Background(), svc, game, nil) })
	assert.Contains(t, out, "~ Test Mod - OVERRIDDEN BY THE PROFILE (Config/mod.ini: mod file, overridden by the profile)")
	assert.NotContains(t, out, "CHANGED SINCE DEPLOY")

	jsonOutput = true
	outJSON := captureStdout(t, func() error { return doVerify(context.Background(), svc, game, nil) })
	var doc core.VerifyReport
	require.NoError(t, json.Unmarshal([]byte(outJSON), &doc))
	var found bool
	for _, f := range doc.Result.Findings {
		if f.Status == core.VerifyStatusDeployedOverridden {
			found = true
			assert.Equal(t, "Config/mod.ini", f.FileID)
			assert.False(t, f.Fixable)
		}
	}
	assert.True(t, found, "the JSON names the overridden file: %+v", doc.Result.Findings)
	assert.Zero(t, doc.Result.Issues)
	assert.Zero(t, doc.Result.Warnings)
}
