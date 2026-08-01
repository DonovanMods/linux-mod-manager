package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
)

// TestSwitchProgressLine_RendersInstallErrorAndDownloadFailedPhases is the
// RED test for the Fix wave 2 review finding's item 2: switchProgressLine
// used to fall to its `default: return ActionProgress{}, false` branch for
// core.SwitchInstallError/core.SwitchDownloadFailed, so a live install/
// download failure during a NeedsDownloads switch never reached the status
// line at all while the action was still running - only the (also-broken,
// see the sandbox-level RED test in service_core_test.go) post-completion
// Warnings summary had any chance of surfacing it. Mirrors
// installProgressLine's own per-phase-composes-a-line pattern.
func TestSwitchProgressLine_RendersInstallErrorAndDownloadFailedPhases(t *testing.T) {
	installErr := core.DeployProgress{
		Phase: core.SwitchInstallError, SourceID: "src", ModID: "modZ",
		Detail: "failed to fetch mod: connection refused",
	}
	line, ok := switchProgressLine(installErr)
	assert.True(t, ok, "SwitchInstallError must compose a visible progress line, not be dropped")
	assert.Contains(t, line.Line, "src")
	assert.Contains(t, line.Line, "modZ")
	assert.Contains(t, line.Line, "failed to fetch mod: connection refused")

	downloadFailed := core.DeployProgress{
		Phase: core.SwitchDownloadFailed, SourceID: "src", ModID: "modQ", ModName: "Mod Q",
		Detail: "download failed: connection reset",
	}
	line, ok = switchProgressLine(downloadFailed)
	assert.True(t, ok, "SwitchDownloadFailed must compose a visible progress line, not be dropped")
	assert.Contains(t, line.Line, "src")
	assert.Contains(t, line.Line, "modQ")
	assert.Contains(t, line.Line, "download failed: connection reset")
}

// TestSwitchProgressLine_UnhandledPhaseStillDrops pins the "deliberately
// narrows" contract switchProgressLine's own doc comment describes: a phase
// with nothing useful to show a status line (e.g. SwitchDisableNote, an
// internal --verbose-only CLI diagnostic) still falls to the default case.
func TestSwitchProgressLine_UnhandledPhaseStillDrops(t *testing.T) {
	_, ok := switchProgressLine(core.DeployProgress{Phase: core.SwitchDisableNote})
	assert.False(t, ok)
}

// TestInstallProgressLine_RendersCompiling guards #190 item 1's TUI parity:
// the TUI drives the exact same core.ApplyInstall/DeployProgress path as the
// CLI (installProgressLine's own doc comment), so InstallCompiling must
// compose a status line here too, not silently fall to the default case.
func TestInstallProgressLine_RendersCompiling(t *testing.T) {
	line, ok := installProgressLine("Bear Mount", core.DeployProgress{Phase: core.InstallCompiling})
	assert.True(t, ok, "InstallCompiling must compose a visible progress line, not be dropped")
	assert.Contains(t, line.Line, "Bear Mount")
	assert.Contains(t, line.Line, "compiling")
}
