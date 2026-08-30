package core_test

import (
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

// TestEnumString_NegativeValueUsesEscapeHatch pins M1: a negative enum value
// must render the same "<type>(%d)" escape hatch as an out-of-range positive
// one, not panic — these are MarshalText implementations, and the moment
// UnmarshalText is fed hostile input the in-range-only guard stops being
// structural.
func TestEnumString_NegativeValueUsesEscapeHatch(t *testing.T) {
	tests := []struct {
		name string
		fn   func() string
		want string
	}{
		{"DeployPhase", func() string { return core.DeployPhase(-1).String() }, "deploy_phase(-1)"},
		{"DeployModClass", func() string { return core.DeployModClass(-1).String() }, "deploy_mod_class(-1)"},
		{"VerifyEventKind", func() string { return core.VerifyEventKind(-1).String() }, "verify_event_kind(-1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			assert.NotPanics(t, func() { got = tt.fn() })
			assert.Equal(t, tt.want, got)
		})
	}
}

// phasesOf projects recorded events to their phases, for ordered assertions.
// It also returns the same events filtered down to just the FlowEvents
// (phases[i] is the phase of flowEvents[i]), so a caller that needs both an
// index into the phase sequence and the underlying event at that index -
// e.g. to inspect a ModEvent's Detail after locating it by phase - indexes
// flowEvents rather than the original, unfiltered slice: the two coincide
// only when nothing non-FlowEvent appears mid-stream.
func phasesOf(events []core.Event) (phases []core.DeployPhase, flowEvents []core.Event) {
	for _, e := range events {
		if fe, ok := e.(core.FlowEvent); ok {
			phases = append(phases, fe.FlowPhase())
			flowEvents = append(flowEvents, e)
		}
	}
	return phases, flowEvents
}
