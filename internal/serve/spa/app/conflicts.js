// conflicts.js - the words every surface that renders a core.ConflictModRef
// uses, shared so they cannot drift (I-4, epic live review).
//
// The drift this exists to stop was real and one-sided: the Conflicts card
// rendered a bare "(stale)" where `lmm conflicts` renders
// "(stale — redeploy to apply)" (cmd/lmm/conflicts.go). The web told the
// user something was wrong and not what to do about it; the CLI did both,
// for the identical document. Small, but it is the difference between a
// dead end and a next step, on the one card whose whole job is to name a
// next step.
//
// verify.js is the model: pure functions over a frozen core type, imported
// by every surface that renders it, with no state and no DOM of its own.

/**
 * staleWinnerNote is what a stale conflict means, in the CLI's own words
 * (cmd/lmm/conflicts.go's "(stale — redeploy to apply)").
 *
 * "Stale" here means the file's CURRENT deployed provider is not the one
 * the profile's load order now names - which is a fact about the deploy,
 * not about the conflict, so the sentence has to say what closes the gap.
 */
export const staleWinnerNote = "stale — redeploy to apply";

/**
 * conflictLabel names the contenders AND the winning rule (design doc:
 * "each conflict names the contenders and the winning rule"), suffixing the
 * stale note when the deployed reality has not caught up with the order.
 *
 * Built as one plain string rather than split across template-literal
 * lines, which htm's JSX-style whitespace collapsing would otherwise eat
 * between two adjacent interpolations (a real trap: a `trunk fmt` reflow
 * silently dropped the space that used to separate "wins:" from the name).
 */
export function conflictLabel(c) {
  const also = c.also_in.map((m) => m.name).join(", ");
  const label = `${c.owner.name} ↔ ${also} · wins: ${c.load_order_winner.name}`;
  return c.stale ? `${label} (${staleWinnerNote})` : label;
}

/**
 * conflictPairs is the directed mod pairs that make conflict c known (issue
 * 668): its load-order winner over every other provider - the pairs "Mark
 * as intended" marks and "Unmark" unmarks, as core's own rule
 * (conflicts.go's pathKnown) reads them.
 */
export function conflictPairs(c) {
  const winner = c.load_order_winner.key;
  return [c.owner, ...c.also_in]
    .filter(
      (m, i, all) =>
        m.key !== winner && all.findIndex((o) => o.key === m.key) === i,
    )
    .map((m) => ({ winner, loser: m.key }));
}

/** knownConflictNote is what a known conflict means (issue 668). */
export const knownConflictNote = "known — marked as intended";

/**
 * conflictRole is one mod's side of conflict c, for the surfaces that list
 * a mod's own conflicts (the mod panel, the full mod page): whether it
 * wins, and whether the conflict is known.
 */
export function conflictRole(c, key) {
  const role =
    c.load_order_winner.key === key
      ? "wins"
      : `loses to ${c.load_order_winner.name}`;
  return c.known ? ` (${role} · known)` : ` (${role})`;
}
