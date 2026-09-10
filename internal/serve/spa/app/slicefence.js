// slicefence.js - the per-key freshness fence over the store's slices.
//
// main.js runs two fences and they answer different questions. hydrateSeq
// (main.js) answers "is the ROUTE this answer was fetched for still the one
// on screen": a document fetched for a profile the user has since left must
// never land. This one answers the question that fence cannot see - two
// loads of the SAME slice, both for the route still on screen, resolving in
// whichever order the network hands them back (issue 370).
//
// That is not a hypothetical race: every mod-setting write re-reads
// /api/v1/mods when it lands (main.js#refreshAfterModSetting) and every
// finished job re-hydrates the whole route, so a user clicking twice - a
// row's Enabled checkbox, then another row's Lock - leaves two GETs of the
// same slice in flight. Under the route fence alone both committed, so the
// OLDER answer could land last and put the pre-lock library back: the badge
// never appeared and nothing re-fetched, leaving the UI stale until some
// unrelated hydrate.
//
// The rule is the smallest one that works: a load CLAIMS the next number
// for every slice it intends to write, and commits only the slices whose
// claim is still the newest for their key. Claiming is what invalidates - a
// later claim silently retires every earlier one - so the answer asked for
// LAST always wins, whatever order the responses arrive in, and a late one
// is dropped rather than applied. Per-key rather than global, because two
// loads of DIFFERENT slices never conflict: a health re-run must not throw
// away the library reload issued a moment after it.
//
// The one thing that rule cannot do on its own is notice a claim nobody
// ever writes. A load that gives up - its fetch failed, or the page it was
// for is no longer on screen - would otherwise keep every earlier load's
// answer out for good, and a slice whose newest claim wrote nothing and
// whose older, SUCCESSFUL answer was dropped for it keeps a document older
// than both: issue 370's own symptom, reached through the fix for it. So a
// load that abandons a claim RELEASES it, handing the key back to the
// newest claim still in flight. Releasing is deliberately not an
// "unclaim-anything" primitive: it rolls a key back only while the
// abandoning claim is still the newest on it, so a load that has already
// been superseded cannot resurrect itself and reopen the race.

/**
 * Creates a fence over `store`. One per application - the numbers are the
 * fence's own state, and two fences over one store would each believe their
 * claims were the newest.
 */
export function createSliceFence(store) {
  const latest = new Map();

  /**
   * Claims the next number for each of `keys`, invalidating every claim on
   * those keys still in flight. Called when the requests are ISSUED, not
   * when they resolve, so claim order is request order.
   */
  function claim(keys) {
    const claimed = new Map();
    for (const key of keys) {
      const next = (latest.get(key) ?? 0) + 1;
      latest.set(key, next);
      claimed.set(key, next);
    }
    return claimed;
  }

  /**
   * Hands back the keys of `claimed` that are still its own, so the newest
   * claim still in flight owns them again. Called by a load that will
   * write nothing under this claim - a failed fetch, or an answer for a
   * context that has since gone - and a no-op for any key a later claim
   * has already taken. A load that HAS written under the claim must not
   * release it: that would put the key back below its own write and let an
   * older answer land on top of it.
   */
  function release(claimed) {
    for (const [key, number] of claimed) {
      if (latest.get(key) === number) latest.set(key, number - 1);
    }
  }

  /**
   * Reports whether `key` is still this claim's to write. A key the claim
   * never took is never stale - it is simply not fenced, which is what lets
   * one commit carry an unfenced slice (the fatal `error`) alongside fenced
   * ones.
   */
  function isCurrent(claimed, key) {
    return !claimed.has(key) || latest.get(key) === claimed.get(key);
  }

  /**
   * Writes the still-current members of `patch` (top-level slices) and
   * `errors` (fetchErrors entries, keyed by the same slice names), and
   * reports whether anything was written.
   *
   * fetchErrors is merged against the store's CURRENT value here rather
   * than by the caller, so a dropped slice's error message is dropped with
   * it and a stale snapshot of the other keys can never ride along.
   */
  function commit(claimed, patch, errors) {
    const next = {};
    let wrote = false;

    for (const [key, value] of Object.entries(patch ?? {})) {
      if (!isCurrent(claimed, key)) continue;
      next[key] = value;
      wrote = true;
    }

    const fresh = Object.entries(errors ?? {}).filter(([key]) =>
      isCurrent(claimed, key),
    );
    if (fresh.length > 0) {
      next.fetchErrors = {
        ...store.get().fetchErrors,
        ...Object.fromEntries(fresh),
      };
      wrote = true;
    }

    if (wrote) store.set(next);
    return wrote;
  }

  return { claim, release, isCurrent, commit };
}
