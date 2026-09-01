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
// with install's AcceptConflicts set (main.js's retryInstallOverwrite,
// issue 331) - the only kind whose ConflictError this failure implies a next
// step for; every other failed kind still renders its details in full with
// no action under them.

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
    job?.kind === "install" &&
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
