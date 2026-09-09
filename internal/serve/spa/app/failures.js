// failures.js - what a failed job offers to do about itself.
//
// The design's rule for the tray is "failures with their next step inline -
// a conflict shows Overwrite? right in the tray" (docs/plans/2026-08-31-
// serve-spa-design.md §Jobs). The next step is decided by the failure
// envelope's TYPED DETAILS, never by matching on its message text: core's
// errors carry a Details() extension exactly so a frontend can branch on
// structure (internal/core/errors.go, and cmd/lmm's own --json envelope),
// and a UI that string-matched "conflict" would break the first time
// someone reworded an error.
//
// Answering one of these is always the same move, and it is v2 Phase 3
// Ruling 1's: Apply never calls back into the frontend, so a mid-flight
// decision is a typed error the caller answers by RE-RUNNING Apply with the
// matching option. In the tray that means re-planning the same mutation
// with AcceptConflicts set (main.js's retryInstallOverwrite, issue 331,
// widened by issue 333 to cover "import_archive" - kind_import_archive.go's
// own doc comment: "the Overwrite affordance answers it by re-running with
// AcceptConflicts", the identical wire field name install already uses) -
// the only two kinds whose ConflictError this failure implies a next step
// for; every other failed kind still renders its details in full with no
// action under them.

// conflictKinds is every plan kind whose *core.ConflictError this failure
// implies "re-plan and set accept_conflicts" for - both take that exact
// wire field, so retryInstallOverwrite's re-plan-and-apply shape (main.js)
// needs no branch of its own between them.
const conflictKinds = new Set(["install", "import_archive"]);

/**
 * nextStepFor returns the affordance job's failure envelope implies, or
 * null when its details name no action this UI knows.
 *
 * `action` is the actions.js entry point that answers it - always fired
 * with job.id, since that is the one thing every caller (the tray) already
 * has in hand; the entry point itself resolves the rest (which origin, what
 * to re-plan) from state main.js alone keeps.
 */
export function nextStepFor(job) {
  const envelope = job?.error ?? {};
  const conflicts = envelope?.details?.conflicts;
  if (
    conflictKinds.has(job?.kind) &&
    Array.isArray(conflicts) &&
    conflicts.length > 0
  ) {
    return {
      action: "overwrite",
      label: `Overwrite ${conflicts.length} file${conflicts.length === 1 ? "" : "s"}?`,
    };
  }
  return null;
}
