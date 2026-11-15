// itemfailures.js - a failed job's per-mod failures (issue 655).
//
// Two documents carry them. A refusal that joins several mods' errors
// (core's AcquireRefusedError: a NexusMods collection's all-or-nothing
// import, a multi-mod install) puts them on the envelope as `items`. An
// import that saved nothing or only some of its mods
// (ProfileImportIncompleteError) carries its whole result as `details`,
// whose `failures` list every item - refused and held back alike - so the
// server sends no second copy as `items`. failuresOf reads whichever there
// is; everything else here only orders and counts them.

// heldBack is core's refusal code for an item that was fine but was not
// written because another item refused (core.RefusalHeldBack).
const heldBack = "held_back";

/** failuresOf is a job summary's per-mod failures (core.ItemFailure), or
 * an empty list: the envelope's items, else a profile import's
 * details.failures. */
export function failuresOf(summary) {
  const envelope = summary?.error;
  if (!envelope) return [];
  if (Array.isArray(envelope.items) && envelope.items.length > 0) {
    return envelope.items;
  }
  const failures = envelope.details?.failures;
  if (summary.kind === "profile_import" && Array.isArray(failures)) {
    return failures;
  }
  return [];
}

/** splitFailures parts items into the ones that failed or refused, in
 * their order, and the ones only held back behind them. */
export function splitFailures(items) {
  const failed = [];
  const held = [];
  for (const item of items ?? []) {
    (item.refusal === heldBack ? held : failed).push(item);
  }
  return { failed, held };
}

/** failureSummary says how many mods failed in one line: "2 mods failed",
 * or, for an import refused with nothing written, "1 mod refused · 299
 * held back". */
export function failureSummary(items) {
  const { failed, held } = splitFailures(items);
  const mods = (n) => `${n} ${n === 1 ? "mod" : "mods"}`;
  const refused = failed.some((item) => item.refusal);
  const head = `${mods(failed.length)} ${refused ? "refused" : "failed"}`;
  return held.length > 0 ? `${head} · ${held.length} held back` : head;
}
