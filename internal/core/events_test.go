package core_test

import (
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventTypes(t *testing.T) {
	scope := core.Scope{Op: core.OpDeploy, Mod: &domain.ModReference{SourceID: "nexusmods", ModID: "42"}, ModName: "Mod", Index: 1, Total: 3}
	tests := []struct {
		ev    core.Event
		typ   string
		phase core.DeployPhase
	}{
		{core.StepEvent{Scope: scope, Phase: core.DeployNote, Detail: "d"}, "step", core.DeployNote},
		{core.DownloadEvent{Scope: scope, Phase: core.DeployDownloading, Percent: 50}, "download", core.DeployDownloading},
		{core.ModEvent{Scope: scope, Phase: core.DeployDeployed, Class: core.DeployModMerged}, "mod", core.DeployDeployed},
		{core.HookEvent{Scope: scope, Phase: core.DeployBeforeAllForced, Stage: "install.before_all", Detail: "x"}, "hook", core.DeployBeforeAllForced},
		{core.WarningEvent{Scope: scope, Phase: core.DeployWarning, Message: "w"}, "warning", core.DeployWarning},
		{core.MergeEvent{Scope: scope, Phase: core.DeployMergeSynced, MergedMods: 2, Artifact: "a.pak"}, "merge", core.DeployMergeSynced},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			assert.Equal(t, tt.typ, tt.ev.EventType())
			fe, ok := tt.ev.(core.FlowEvent)
			require.True(t, ok)
			assert.Equal(t, tt.phase, fe.FlowPhase())
			assert.Equal(t, scope, fe.EventScope())
		})
	}
	uc := core.UpdateCheckEvent{Scope: core.Scope{Op: core.OpUpdateCheck, ModName: "M", Index: 2, Total: 5}, SourceID: "nexusmods"}
	assert.Equal(t, "update_check", uc.EventType())
	_, isFlow := core.Event(uc).(core.FlowEvent)
	assert.False(t, isFlow)
}

func TestMarshalEvent_Envelope(t *testing.T) {
	ev := core.WarningEvent{Scope: core.Scope{Op: core.OpInstall, ModName: "Mod", Index: 1, Total: 1}, Phase: core.InstallWarning, Message: "could not save checksum"}
	b, err := core.MarshalEvent(ev)
	require.NoError(t, err)
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(b, &env))
	assert.Equal(t, "warning", env.Type)
	assert.JSONEq(t, `{"op":"install","mod_name":"Mod","index":1,"total":1,"phase":"install_warning","message":"could not save checksum"}`, string(env.Data))
}

func TestRecordEvents(t *testing.T) {
	sink, got := core.RecordEvents()
	sink(core.StepEvent{Phase: core.DeployNote})
	sink(core.ModEvent{Phase: core.DeployDeployed})
	require.Len(t, *got, 2)
	assert.Equal(t, core.DeployDeployed, (*got)[1].(core.FlowEvent).FlowPhase())
}

func TestDeployPhase_TextRoundTrip(t *testing.T) {
	seen := make(map[string]core.DeployPhase, core.DeployMergeSynced+1)
	for i := core.DeployPurging; i <= core.DeployMergeSynced; i++ {
		text, err := i.MarshalText()
		require.NoError(t, err)
		name := string(text)
		require.NotEmpty(t, name, "phase %d has an empty wire name", int(i))

		if prev, ok := seen[name]; ok {
			t.Fatalf("wire name %q used by both phase %d and phase %d", name, int(prev), int(i))
		}
		seen[name] = i

		var got core.DeployPhase
		require.NoError(t, got.UnmarshalText(text))
		assert.Equal(t, i, got, "round-trip mismatch for phase %d (%q)", int(i), name)
	}
}

// phasesOf projects recorded events to their phases, for ordered assertions.
func phasesOf(events []core.Event) []core.DeployPhase {
	out := make([]core.DeployPhase, 0, len(events))
	for _, e := range events {
		if fe, ok := e.(core.FlowEvent); ok {
			out = append(out, fe.FlowPhase())
		}
	}
	return out
}
