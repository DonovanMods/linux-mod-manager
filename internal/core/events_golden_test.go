package core_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateEventGoldens re-records the event envelope goldens. Run ONCE:
//
//	go test ./internal/core/ -run TestEventGoldens -update-events
//
// After that the files are frozen: they pin core.MarshalEvent's wire
// envelope ({"type":…,"data":…}) for every event type, so any future change
// to an event's JSON shape shows up as a diff here instead of silently
// reaching a future frontend. Named -update-events rather than -update:
// cmd/lmm already registers package-level "-update" (verify_golden_test.go)
// and "-update-modshow" (mod_show_golden_test.go) flags, and Go's flag
// package panics on a duplicate registration within the same package - this
// is a different package (core_test), but the name still follows that
// established disambiguation convention.
var updateEventGoldens = flag.Bool("update-events", false, "rewrite internal/core/testdata/events/*.golden")

// sampleFile is the DownloadableFile every case that carries one uses -
// every field populated, so the golden pins the full shape rather than the
// omitempty branches.
var sampleFile = &domain.DownloadableFile{
	ID: "f1", Name: "Main", FileName: "main.zip", Version: "1.2.3", Size: 1234, IsPrimary: true,
}

// sampleScope is embedded in every case: Op set, a non-nil Mod, ModName, and
// a non-zero Index/Total batch position - the fully-populated shape, not the
// omitempty-heavy common case.
func sampleScope(op core.Op) core.Scope {
	return core.Scope{
		Op:      op,
		Mod:     &domain.ModReference{SourceID: "nexusmods", ModID: "42"},
		ModName: "Sample Mod",
		Index:   2,
		Total:   5,
	}
}

func TestEventGoldens(t *testing.T) {
	tests := []struct {
		name string
		ev   core.Event
	}{
		{
			"step",
			core.StepEvent{
				Scope: sampleScope(core.OpInstall), Phase: core.InstallDownloadStarted,
				Detail: "starting download", File: sampleFile,
			},
		},
		{
			"download",
			core.DownloadEvent{
				Scope: sampleScope(core.OpInstall), Phase: core.InstallDownloading, File: sampleFile,
				Downloaded: 617, TotalBytes: 1234, Percent: 50,
			},
		},
		{
			"mod",
			core.ModEvent{
				Scope: sampleScope(core.OpDeploy), Phase: core.DeployDeployed,
				Detail: "redeployed after cache miss", Version: "1.2.3",
				Class: core.DeployModMerged, FilesExtracted: 7,
			},
		},
		{
			"hook",
			core.HookEvent{
				Scope: sampleScope(core.OpInstall), Phase: core.InstallBeforeAllForced,
				Stage: "install.before_all", Detail: "hook failed, continuing (--force)",
			},
		},
		{
			"warning",
			core.WarningEvent{
				Scope: sampleScope(core.OpInstall), Phase: core.InstallWarning,
				Message: "could not save checksum",
			},
		},
		{
			"merge",
			core.MergeEvent{
				Scope: sampleScope(core.OpDeploy), Phase: core.DeployMergeSynced,
				MergedMods: 3, Artifact: "merged.pak", RawFallbacks: 1,
			},
		},
		{
			"update_check",
			core.UpdateCheckEvent{
				Scope: sampleScope(core.OpUpdateCheck), SourceID: "nexusmods",
				GlobalIndex: 7, GlobalTotal: 12,
			},
		},
		{
			"verify",
			core.VerifyEvent{
				Scope: sampleScope(core.OpVerify), Kind: core.VerifyEvFinding, HasFiles: true,
				Finding: core.VerifyFinding{
					ModID: "42", ModName: "Sample Mod", FileID: "f1", Status: "ok",
					Note: "re-downloaded to populate checksum", Recorded: "1.2.2", Effective: "1.2.3", Version: "1.2.3",
				},
				Recorded: "1.2.2", Effective: "1.2.3", Version: "1.2.3",
				ExpectedCount: 3, ChecksumPopulated: true,
				Detail: "Repaired: 1.2.2 → 1.2.3", Fixed: true,
			},
		},
	}

	// Exhaustiveness guard (M5): every known Event implementation's
	// EventType() must have exactly one row above, so a ninth event type
	// added without a table row (and thus no golden) is caught here rather
	// than silently shipping ungoldened, and two rows colliding on the same
	// EventType() (which would silently overwrite each other's golden file)
	// fails loudly instead.
	wantEventTypes := []string{
		core.StepEvent{}.EventType(),
		core.DownloadEvent{}.EventType(),
		core.ModEvent{}.EventType(),
		core.HookEvent{}.EventType(),
		core.WarningEvent{}.EventType(),
		core.MergeEvent{}.EventType(),
		core.UpdateCheckEvent{}.EventType(),
		core.VerifyEvent{}.EventType(),
	}
	seenTypes := make(map[string]bool, len(tests))
	for _, tt := range tests {
		et := tt.ev.EventType()
		require.Falsef(t, seenTypes[et], "two table rows share EventType() %q", et)
		seenTypes[et] = true
	}
	require.Len(t, seenTypes, len(wantEventTypes), "table's distinct EventType() count drifted from the known event types")
	for _, et := range wantEventTypes {
		assert.Truef(t, seenTypes[et], "no golden table row for event type %q", et)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := core.MarshalEvent(tt.ev)
			require.NoError(t, err)

			path := filepath.Join("testdata", "events", tt.ev.EventType()+".golden")
			if *updateEventGoldens {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, append(b, '\n'), 0o644))
				return
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "golden %s missing - record it with -update-events BEFORE relying on this test", path)
			assert.Equal(t, string(want), string(b)+"\n", "%s event envelope drifted from the recorded golden", tt.ev.EventType())

			var env struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			require.NoError(t, json.Unmarshal(b, &env))
			assert.Equal(t, tt.ev.EventType(), env.Type)

			var data map[string]any
			require.NoError(t, json.Unmarshal(env.Data, &data), "%s event data must be a JSON object", tt.ev.EventType())
		})
	}
}
