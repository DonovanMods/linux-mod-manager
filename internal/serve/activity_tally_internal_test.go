package serve

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue 364: a finished batch job's outcome tally is computed server-side
// and carried on its summary, so the control that started the job has it
// on the SAME frame that says the job is over - rather than saying "Done"
// until a second read of the result document corrects it.

func TestTallyOfResult_Updates(t *testing.T) {
	boots := core.UpdateApplyResult{
		Mod:    domain.ModReference{SourceID: "fake", ModID: "boots"},
		Name:   "Better Boots",
		Reason: "locked at v1.0",
	}
	gloves := core.UpdateApplyResult{Mod: domain.ModReference{SourceID: "fake", ModID: "gloves"}, Name: "Great Gloves"}

	tests := []struct {
		name   string
		result any
		want   *jobTally
	}{
		{
			// The locked-skip document: applied and skipped rows and no
			// "failed" member at all (omitzero) - the exact shape that once
			// fell through to a bare "Done" (I1, unit 8 gate review).
			name: "applied and skipped",
			result: &core.UpdateBatchResult{
				Applied: []core.UpdateApplyResult{gloves},
				Skipped: []core.UpdateApplyResult{boots},
			},
			want: &jobTally{Kind: "updates", Applied: 1, Skipped: 1,
				SkippedNotes: []string{"Better Boots — locked at v1.0"}},
		},
		{
			name: "applied and failed",
			result: &core.UpdateBatchResult{
				Applied: []core.UpdateApplyResult{gloves},
				Failed:  []core.UpdateBatchFailure{{}, {}},
			},
			want: &jobTally{Kind: "updates", Applied: 1, Failed: 2},
		},
		{
			// Every item failed: the job still SUCCEEDED, which is the
			// whole reason the tally exists.
			name:   "nothing applied",
			result: &core.UpdateBatchResult{Failed: []core.UpdateBatchFailure{{}}},
			want:   &jobTally{Kind: "updates", Failed: 1},
		},
		{
			// A skipped row with no reason still names itself, and one with
			// no name falls back to its mod id, then to "a mod".
			name: "skipped notes fall back",
			result: &core.UpdateBatchResult{Skipped: []core.UpdateApplyResult{
				{Name: "Named"},
				{Mod: domain.ModReference{ModID: "m7"}, Reason: "why"},
				{},
			}},
			want: &jobTally{Kind: "updates", Skipped: 3,
				SkippedNotes: []string{"Named", "m7 — why", "a mod"}},
		},
		{
			// Nothing applied, failed or skipped: nothing honest to report
			// over a bare "Done".
			name:   "all zero",
			result: &core.UpdateBatchResult{Applied: []core.UpdateApplyResult{}},
			want:   nil,
		},
		{
			name:   "profile import",
			result: &core.ProfileImportResult{Installed: 2, Failed: 1, Skipped: 3, Warnings: []string{"w"}},
			want:   &jobTally{Kind: "profile_import", Installed: 2, Failed: 1, Skipped: 3, Warnings: 1},
		},
		{
			// Every pending mod left uninstalled - the flow's documented
			// default, a skip and not a failure.
			name:   "profile import skipped only",
			result: &core.ProfileImportResult{Skipped: 2},
			want:   &jobTally{Kind: "profile_import", Skipped: 2},
		},
		{
			name:   "profile import all zero",
			result: &core.ProfileImportResult{ProfileName: "default"},
			want:   nil,
		},
		{
			// A kind whose result is not a batch says "Done" - including
			// one whose fields happen to be called installed/failed.
			name:   "deploy",
			result: &core.DeployResult{Deployed: 3},
			want:   nil,
		},
		{name: "no result", result: nil, want: nil},
		{name: "nil batch pointer", result: (*core.UpdateBatchResult)(nil), want: nil},
		{name: "nil import pointer", result: (*core.ProfileImportResult)(nil), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tallyOfResult(tt.result))
		})
	}
}

// TestJobRegistry_FinishedSummaryCarriesTheTally is the property the SPA
// depends on: the job_done frame's summary, the index row and the status
// document all carry the tally the moment the job is terminal.
func TestJobRegistry_FinishedSummaryCarriesTheTally(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)
	_, live, cancel := r.watch(16)
	defer cancel()

	id, err := r.Start("updates", func(context.Context, core.EventSink) (any, error) {
		return &core.UpdateBatchResult{
			Applied: []core.UpdateApplyResult{{Name: "Great Gloves"}},
			Skipped: []core.UpdateApplyResult{{Name: "Better Boots", Reason: "locked"}},
		}, nil
	})
	require.NoError(t, err)
	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	want := &jobTally{Kind: "updates", Applied: 1, Skipped: 1,
		SkippedNotes: []string{"Better Boots — locked"}}

	var done *jobSummary
	for done == nil {
		ev := <-live
		if ev.Name == activityDoneEvent {
			s := ev.Payload.(jobSummary)
			done = &s
		}
	}
	assert.Equal(t, jobSucceeded, done.State)
	assert.Equal(t, want, done.Tally, "the job_done frame must carry the tally itself")
	assert.Equal(t, want, r.list()[0].Tally, "and so must the index row")
	assert.Equal(t, want, j.status().Tally, "and the status document a reconnect reads")
}

// TestJobRegistry_RunningAndFailedJobsCarryNoTally: there is no outcome to
// count until the batch has returned one.
func TestJobRegistry_RunningAndFailedJobsCarryNoTally(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 4)

	release := make(chan struct{})
	running, err := r.Start("updates", func(context.Context, core.EventSink) (any, error) {
		<-release
		return &core.UpdateBatchResult{Applied: []core.UpdateApplyResult{{Name: "x"}}}, nil
	})
	require.NoError(t, err)
	rj, _ := r.job(running)
	assert.Nil(t, rj.summary().Tally)
	close(release)
	waitFor(t, rj.done(), "running job completion")

	failed, err := r.Start("updates", func(context.Context, core.EventSink) (any, error) {
		return nil, assert.AnError
	})
	require.NoError(t, err)
	fj, _ := r.job(failed)
	waitFor(t, fj.done(), "failed job completion")
	assert.Nil(t, fj.summary().Tally)
}
