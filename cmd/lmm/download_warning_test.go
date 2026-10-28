package main

// #425: a download-time warning reaches the terminal whichever command
// downloaded - install, update, deploy, profile switch/apply/import,
// snapshot restore, import - because every one of them hands core its
// console closure through quietSink, which prints the warning itself.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuietSink_PrintsADownloadWarningForEveryCommand(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	var passed []core.Event
	sink := quietSink(func(e core.Event) { passed = append(passed, e) })

	_, stderr, err := captureStdoutAndStderr(t, func() error {
		sink(core.WarningEvent{Scope: core.Scope{Op: core.OpDeploy, ModName: "Jotunn"}, Phase: core.DownloadWarning,
			Message: "BepInEx found in /g; declare it with `lmm game edit valheim --loader bepinex`"})
		sink(core.WarningEvent{Scope: core.Scope{Op: core.OpDeploy}, Phase: core.DeployWarning, Message: "the command's own"})
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "Warning: Jotunn: BepInEx found in /g; declare it with `lmm game edit valheim --loader bepinex`\n", stderr)
	require.Len(t, passed, 1, "the download warning is printed once, here, and not handed on")
	assert.Equal(t, core.DeployWarning, passed[0].(core.WarningEvent).Phase, "every other event reaches the command's own closure")
}

func TestQuietSink_StaysNilUnderJSON(t *testing.T) {
	withJSONOutput(t)
	assert.Nil(t, quietSink(func(core.Event) {}), "Ruling 15: no events under --json")
}
