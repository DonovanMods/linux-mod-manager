package serve

import "github.com/DonovanMods/linux-mod-manager/v2/internal/core"

// jobTally is a finished BATCH job's honest outcome count, carried on its
// jobSummary (and its jobStatus) so the control that started the job can
// render it on the same frame that says the job is over (issue 364).
//
// It exists because a batch job's own State is "succeeded" the instant its
// Apply returns without an ERROR - true for "updates" (applyUpdatesKind)
// and "profile_import" (core.ApplyImport) even when EVERY item inside them
// failed, since both record per-item outcomes in their result rather than
// returning one. The SPA used to derive this from a second read of the
// result document (GET /api/v1/jobs/{id}), so a control showed a bare
// "Done" for the frames between the job_done frame and that read's answer.
// Computing it here, once, when the job finishes, is what lets the first
// terminal render already be the right one - including on a control whose
// container unmounts on completion, where waiting for a second read would
// lose the outcome entirely.
//
// Two shapes share the type, told apart by Kind, and are kept SEPARATE
// rather than collapsed into one two-number tally:
//
//   - "updates" (core.UpdateBatchResult): Applied, Failed, Skipped and
//     SkippedNotes. Skipped is the batch declining to try - a locked ref
//     (#97), an item Steam updates itself (#269) - which is not a failure.
//   - "profile_import" (core.ProfileImportResult): Installed, Failed,
//     Skipped and Warnings. Skipped is the flow's own documented default
//     (the plan's "Download and install" left unchecked), not anything
//     that went wrong.
//
// The shape-specific counts are omitzero, so a client reads an absent one
// as zero; Failed and Skipped are common to both and always present.
type jobTally struct {
	// Kind is the result shape: "updates" or "profile_import".
	Kind string `json:"kind"`

	// Applied counts an update batch's completed items.
	Applied int `json:"applied,omitzero"`
	// Installed counts a profile import's installed mods.
	Installed int `json:"installed,omitzero"`
	// Failed counts the items that were tried and did not complete.
	Failed int `json:"failed"`
	// Skipped counts the items the job declined to attempt.
	Skipped int `json:"skipped"`
	// Warnings counts a profile import's result warnings (one per failed
	// mod).
	Warnings int `json:"warnings,omitzero"`

	// SkippedNotes is one sentence per skipped update item, in the engine's
	// OWN words: the name a user recognises plus core.UpdateApplyResult's
	// Reason (a lock's refusal, Steam's "it updates this itself"). "1
	// skipped" alone invites a guess; the note is what makes the count
	// honest.
	SkippedNotes []string `json:"skipped_notes,omitempty"`
}

// tallyOfResult is a finished job's result document's jobTally, or nil for
// a result that is not a batch (a deploy, a single install, an enable) or
// a batch that applied, failed and skipped nothing at all - which has
// nothing honest to report over a bare "Done".
//
// It switches on the result's Go TYPE, the closed set of core documents
// that carry per-item outcomes, so it never misreads an unrelated result
// whose fields merely share these names (core.InstallResult's own
// installed/failed are lists of a different meaning).
func tallyOfResult(result any) *jobTally {
	switch r := result.(type) {
	case *core.UpdateBatchResult:
		if r == nil || len(r.Applied)+len(r.Failed)+len(r.Skipped) == 0 {
			return nil
		}
		tally := &jobTally{
			Kind:    "updates",
			Applied: len(r.Applied),
			Failed:  len(r.Failed),
			Skipped: len(r.Skipped),
		}
		for _, item := range r.Skipped {
			tally.SkippedNotes = append(tally.SkippedNotes, skippedNote(item))
		}
		return tally
	case *core.ProfileImportResult:
		if r == nil || r.Installed+r.Failed+r.Skipped == 0 {
			return nil
		}
		return &jobTally{
			Kind:      "profile_import",
			Installed: r.Installed,
			Failed:    r.Failed,
			Skipped:   r.Skipped,
			Warnings:  len(r.Warnings),
		}
	}
	return nil
}

// skippedNote is one declined update item in words: its name (or, lacking
// one, its mod id) and the engine's reason when it gave one. A row that
// somehow carries neither still names itself as "a mod".
func skippedNote(item core.UpdateApplyResult) string {
	name := item.Name
	if name == "" {
		name = item.Mod.ModID
	}
	if name == "" {
		name = "a mod"
	}
	if item.Reason == "" {
		return name
	}
	return name + " — " + item.Reason
}
