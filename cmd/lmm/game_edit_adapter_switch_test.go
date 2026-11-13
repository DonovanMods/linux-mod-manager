package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #606 review fix 1: `lmm game edit --adapter` on a game with installed
// mods says they keep the layout they were installed with - the same
// sentence verify and the game page show - and it is a warning, so in
// --json mode it goes to stderr, leaving the document alone on stdout.
func TestGameEdit_AdapterSwitchWarnsAboutInstalledMods(t *testing.T) {
	svc := setupGameEditTest(t)
	app.RegisterAdapters(svc)
	adapterBefore := gameEditAdapter
	t.Cleanup(func() { gameEditAdapter = adapterBefore })
	ctx := context.Background()

	// No installed mods: no warning.
	gameEditAdapter = "bethesda"
	out := captureStdout(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", true) })
	assert.Contains(t, out, "adapter set to")
	assert.NotContains(t, out, "keep the layout")

	// Back to the identity, then install a mod and switch again.
	gameEditAdapter = ""
	captureStdout(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", true) })
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "1", SourceID: "nexusmods", Name: "SkyUI", Version: "1.0", GameID: "skyrim-se"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	gameEditAdapter = "bethesda"
	stdout, stderr, err := captureStdoutAndStderr(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", true) })
	require.NoError(t, err)
	assert.Contains(t, stdout, "adapter set to")
	assert.Contains(t, stderr, "warning: 1 mod(s) already installed for this game keep the layout they were installed with")
	assert.Contains(t, stderr, "uninstall it")

	// --json: the document alone on stdout, the warning on stderr.
	gameEditAdapter = ""
	captureStdout(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", true) })
	withJSONOutput(t)
	gameEditAdapter = "bethesda"
	stdout, stderr, err = captureStdoutAndStderr(t, func() error { return doGameEdit(ctx, svc, "skyrim-se", true) })
	require.NoError(t, err)
	assert.Contains(t, stdout, `"adapter": "bethesda"`)
	assert.NotContains(t, stdout, "keep the layout")
	assert.Contains(t, stderr, "keep the layout they were installed with")
}
