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
// with (say) install's AcceptConflicts set - which is the install kind's,
// and lands with it in Unit 5.
//
// Until then a next step is rendered but disabled, carrying the name of the
// unit that wires it. That is deliberate: the details are always shown in
// full either way, and an affordance that says what it is waiting for is
// more honest than a failure with nothing under it.

/**
 * nextStepFor returns the affordance a failure envelope implies, or null
 * when its details name no action this UI knows.
 *
 * The returned `pending` is the reason the action is not live yet; a later
 * unit that wires the action returns the same shape with pending null and
 * an `options` payload for the re-run.
 */
export function nextStepFor(envelope) {
  const conflicts = envelope?.details?.conflicts;
  if (Array.isArray(conflicts) && conflicts.length > 0) {
    return {
      action: "overwrite",
      label: `Overwrite ${conflicts.length} file${conflicts.length === 1 ? "" : "s"}?`,
      pending: "Wired when install lands in Unit 5",
    };
  }
  return null;
}
