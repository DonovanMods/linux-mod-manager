package serve

// JSON contract goldens for internal/serve's OWN wire types
// (docs/plans/2026-08-30-serve-impl.md Task 7). Everything under
// /api/v1 that is a frozen core document is already goldened in
// internal/core; what is pinned HERE is the small set of documents serve
// itself defines - the job status document, the plan and job-start
// request/response envelopes, and the error envelope every failure uses.
// They are wire surface a client parses, so they get the same treatment
// every other wire type in this repo gets: a recorded golden, plus the
// go/ast ratchet (json_contract_coverage_test.go) that fails the build if a
// serve type gains json tags without gaining one.

import (
	"bytes"
	"encoding/json/jsontext"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// updateServeJSONGoldens re-records this package's wire goldens. Run ONCE:
//
//	go test ./internal/serve/ -run TestServeJSONGoldens -update-serve-json-goldens
//
// After that the files are frozen. Named -update-serve-json-goldens rather
// than -update for the same reason every other package's flag has its own
// name (internal/core's -update-json-goldens, cmd/lmm's -update): a
// re-record must only ever touch the goldens the author meant to change.
var updateServeJSONGoldens = flag.Bool("update-serve-json-goldens", false, "rewrite internal/serve/testdata/json/*.golden")

// goldenTime is the fixed timestamp every golden with a time field uses, so
// a golden diff is never just "the clock moved".
var goldenTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

// goldenDeployResult is the core result document the succeeded-job goldens
// carry. internal/core already pins DeployResult's own shape; it appears
// here only so jobStatus's "result" member is pinned as carrying the core
// document verbatim rather than some serve-side re-rendering of it.
var goldenDeployResult = &core.DeployResult{
	Deployed:       2,
	Skipped:        []core.InstalledRef{{SourceID: "fake", ModID: "m2", Name: "Mod Two", Reason: "disabled"}},
	Warnings:       []string{"could not sync merged pak"},
	Notes:          []string{"1 mod was already deployed"},
	MergedArtifact: "zzz-lmm-merged.pak",
	MergedMods:     3,
	RawFallbacks:   1,
}

func TestServeJSONGoldens(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{
			// The succeeded shape: a result document, no error, and the
			// event counters that let a late viewer say whether the history
			// it can still replay is complete.
			"job_status",
			jobStatus{
				ID:            "0123456789abcdef0123456789abcdef",
				Kind:          "deploy",
				State:         jobSucceeded,
				StartedAt:     goldenTime,
				EndedAt:       goldenTime.Add(3 * time.Second),
				Result:        goldenDeployResult,
				EventCount:    12,
				DroppedEvents: 4,
			},
		},
		{
			// The failed shape: no result, and the same {"error","details"}
			// envelope /api/v1's synchronous failures use - typed details
			// included, so a client parses one failure shape everywhere.
			"job_status_failed",
			jobStatus{
				ID:        "fedcba9876543210fedcba9876543210",
				Kind:      "install",
				State:     jobFailed,
				StartedAt: goldenTime,
				EndedAt:   goldenTime.Add(time.Second),
				Error: &apiErrorEnvelope{
					Error: "file conflicts detected",
					Details: (&core.ConflictError{Conflicts: []core.Conflict{{
						RelativePath:    "Mods/a.pak",
						CurrentSourceID: "fake",
						CurrentModID:    "m9",
					}}}).Details(),
				},
				EventCount: 3,
			},
		},
		{
			// A running job: no result, no error, no end time.
			"job_status_running",
			jobStatus{
				ID:         "aaaabbbbccccddddeeeeffff00001111",
				Kind:       "deploy",
				State:      jobRunning,
				StartedAt:  goldenTime,
				EventCount: 1,
			},
		},
		{
			"plan_response",
			planResponse{
				PlanID: "0123456789abcdef0123456789abcdef",
				Kind:   "deploy",
				Plan: &core.DeployPlan{
					Profile: "default",
					Mods: []core.DeployPlanMod{{
						Ref:  domain.ModReference{SourceID: "fake", ModID: "m1", Version: "1.0"},
						Name: "Mod One",
						Link: []string{"Mods/one.pak"},
					}},
					Purge: []string{},
					Hooks: []string{},
				},
			},
		},
		{
			"plan_kind_details",
			planKindDetails{SupportedKinds: supportedPlanKinds()},
		},
		{
			"job_start_request",
			jobStartRequest{
				PlanID:  "0123456789abcdef0123456789abcdef",
				Options: jsontext.Value(`{"accept_conflicts":true}`),
			},
		},
		{
			"job_start_response",
			jobStartResponse{JobID: "fedcba9876543210fedcba9876543210"},
		},
		{
			"deploy_plan_request",
			deployPlanRequest{
				Purge:      true,
				All:        true,
				ModID:      "m1",
				SourceID:   "fake",
				LinkMethod: "hardlink",
				Force:      true,
				SkipHooks:  true,
			},
		},
		{
			// #225's install options, both halves: what the PLAN previews
			// (which version's files the candidate pool is drawn from) and
			// what the confirm page finally applies - including the
			// conflict answer v2 Phase 3 Ruling 1 says a caller re-runs
			// Apply with.
			"install_plan_request",
			installPlanRequest{
				SourceID:     "fake",
				ModID:        "m2",
				Version:      "2.0",
				ShowArchived: true,
			},
		},
		{
			"install_apply_request",
			installApplyRequest{
				Version:         "2.0",
				FileIDs:         []string{"f1", "f2"},
				AcceptConflicts: true,
				Force:           true,
				SkipHooks:       true,
			},
		},
		{
			// #226's uninstall options, both halves: what the PLAN is
			// computed with, and what the confirm page's checkboxes finally
			// apply.
			"uninstall_plan_request",
			uninstallPlanRequest{
				ModID:     "m1",
				SourceID:  "fake",
				KeepCache: true,
				SkipHooks: true,
			},
		},
		{
			"uninstall_apply_request",
			uninstallApplyRequest{KeepCache: true, Force: true, SkipHooks: true},
		},
		{
			// #74's batch, both halves of its options plus the two
			// documents it owns: there is no core batch flow (`lmm update`
			// loops over one-mod plans), so the SELECTION and the
			// per-mod report are serve's own wire surface.
			"updates_plan_request",
			updatesPlanRequest{Mods: []string{"fake:m1", "fake:m2"}},
		},
		{
			"updates_apply_request",
			updatesApplyRequest{Force: true, SkipHooks: true},
		},
		{
			"updates_batch_plan",
			updatesBatchPlan{
				GameID:  "g1",
				Profile: "default",
				Updates: []domain.Update{{
					InstalledMod: domain.InstalledMod{
						Mod:         domain.Mod{ID: "m1", SourceID: "fake", Name: "Mod One", Version: "1.0", GameID: "g1"},
						ProfileName: "default",
						Enabled:     true,
					},
					NewVersion: "2.0",
				}},
				NotFound: []string{"fake:m9"},
			},
		},
		{
			"updates_batch_result",
			updatesBatchResult{
				Applied: []core.UpdateApplyResult{{
					Mod:         domain.ModReference{SourceID: "fake", ModID: "m1"},
					Name:        "Mod One",
					FromVersion: "1.0",
					ToVersion:   "2.0",
					Status:      core.UpdateUpdated,
				}},
				Failed: []updateBatchFailure{{Mod: "fake:m2", Name: "Mod Two", Error: "mod is locked"}},
			},
		},
		{
			"update_batch_failure",
			updateBatchFailure{Mod: "fake:m2", Name: "Mod Two", Error: "mod is locked"},
		},
		{
			// The two profile flows' plan requests. Neither has an apply
			// request with anything in it - ApplyProfileSwitch and
			// ProfileApplyOptions both take no options - so their empty
			// structs carry no json tags and pin nothing.
			"switch_plan_request",
			switchPlanRequest{Profile: "modded"},
		},
		{
			"profile_apply_plan_request",
			profileApplyPlanRequest{Profile: "modded"},
		},
		{
			"api_error_envelope",
			apiErrorEnvelope{
				Error:   "profile switch finished with warnings",
				Details: (&core.ProfileWarningsError{Warnings: []string{"1 mod could not be undeployed"}}).Details(),
			},
		},
		{
			"selection_error_details",
			selectionErrorDetails{
				Games: []core.GameListEntry{{
					Game: domain.Game{
						ID: "g1", Name: "Fixture Game",
						InstallPath: "/games/g1", ModPath: "/games/g1/Mods",
						LinkMethod: domain.LinkSymlink,
					},
					Default: true,
				}},
				Profiles: []string{"default", "modded"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, core.EncodeJSON(&buf, tc.value))

			path := filepath.Join("testdata", "json", tc.name+".golden")
			if *updateServeJSONGoldens {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
				return
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "missing golden - re-record with -update-serve-json-goldens")
			require.Equal(t, string(want), buf.String())
		})
	}
}
