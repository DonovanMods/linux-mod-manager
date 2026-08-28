package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLineOf_ProjectsEveryFlowEventType(t *testing.T) {
	scope := core.Scope{Op: core.OpInstall, Mod: &domain.ModReference{SourceID: "s", ModID: "m"}, ModName: "N", Index: 2, Total: 5}
	file := &domain.DownloadableFile{ID: "f"}
	tests := []struct {
		name string
		ev   core.Event
		want flowLine
	}{
		{"step", core.StepEvent{Scope: scope, Phase: core.InstallNote, Detail: "d", File: file}, flowLine{Index: 2, Total: 5, ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallNote, Detail: "d", File: file}},
		{"download", core.DownloadEvent{Scope: scope, Phase: core.InstallDownloading, File: file, Downloaded: 3, TotalBytes: 6, Percent: 50}, flowLine{Index: 2, Total: 5, ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallDownloading, File: file, Downloaded: 3, TotalBytes: 6, Percent: 50}},
		{"mod", core.ModEvent{Scope: scope, Phase: core.InstallDepInstalled, Detail: "r", Version: "1.0", Class: core.DeployModRaw, FilesExtracted: 4}, flowLine{Index: 2, Total: 5, ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallDepInstalled, Detail: "r", ModVersion: "1.0", ModClass: core.DeployModRaw, FilesExtracted: 4}},
		{"hook", core.HookEvent{Scope: scope, Phase: core.InstallBeforeAllForced, Stage: "install.before_all", Detail: "boom"}, flowLine{Index: 2, Total: 5, ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallBeforeAllForced, Detail: "boom"}},
		{"warning", core.WarningEvent{Scope: scope, Phase: core.InstallWarning, Message: "w"}, flowLine{Index: 2, Total: 5, ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallWarning, Detail: "w"}},
		{"merge", core.MergeEvent{Scope: core.Scope{Op: core.OpDeploy}, Phase: core.DeployMergeSynced, MergedMods: 3, Artifact: "a.pak", RawFallbacks: 1}, flowLine{Total: 3, Phase: core.DeployMergeSynced, Detail: "a.pak", RawFallbacks: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lineOf(tt.ev)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}
	_, ok := lineOf(core.UpdateCheckEvent{})
	assert.False(t, ok, "non-flow events are not lines")
}

// TestLineOf_NarrowedPayloads pins the two table-mandated payload narrowings
// task-12-review's M4 flagged: InstallDownloadFailed carries no File (its
// only reader, install.go's InstallDownloadFailed case, reads only Detail),
// and batch ModEvent phases other than InstallDepInstalling carry no
// ModVersion (its only reader, install.go's InstallDepInstalling case, is a
// different phase). Both are harmless today because the closures never read
// the missing field on these phases - if a future closure edit starts
// reading File or ModVersion here, this test documents that lineOf will
// hand it a zero value, not silently regress that reader.
func TestLineOf_NarrowedPayloads(t *testing.T) {
	scope := core.Scope{Op: core.OpInstall, Mod: &domain.ModReference{SourceID: "s", ModID: "m"}, ModName: "N"}

	t.Run("InstallDownloadFailed has no File", func(t *testing.T) {
		got, ok := lineOf(core.ModEvent{Scope: scope, Phase: core.InstallDownloadFailed, Detail: "boom"})
		require.True(t, ok)
		assert.Equal(t, flowLine{ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallDownloadFailed, Detail: "boom"}, got)
		assert.Nil(t, got.File, "InstallDownloadFailed's closure (install.go) reads only Detail; File must stay absent")
	})

	t.Run("batch InstallDepInstalled has no ModVersion", func(t *testing.T) {
		got, ok := lineOf(core.ModEvent{Scope: scope, Phase: core.InstallDepInstalled, FilesExtracted: 4})
		require.True(t, ok)
		assert.Equal(t, flowLine{ModName: "N", ModID: "m", SourceID: "s", Phase: core.InstallDepInstalled, FilesExtracted: 4}, got)
		assert.Empty(t, got.ModVersion, "only InstallDepInstalling's closure case reads ModVersion; InstallDepInstalled must stay empty")
	})
}
