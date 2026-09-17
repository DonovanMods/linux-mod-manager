// indexnotice.js - the one-time wait a search against a cold local index
// costs, said out loud (issue 410, design §2.7 and its approval note: "the
// equivalent line in the web search page's progress area").
//
// A source that searches a LOCAL copy of its catalogue (Thunderstore)
// builds that copy inside the first search, which then takes a few seconds
// instead of a few milliseconds. The terminal prints "Building the
// Thunderstore index for ... (one-time)..." while it waits; this module is
// how the web says the same thing while its search request is pending.
//
// It asks only about indexes the game actually maps, learned from the
// index listing (GET /api/v1/indexes, cached briefly) - never by probing
// every source, because a source that keeps no index answers 404, and a
// 404 is an error line in the browser's console on every search.

import { getSourceIndex, listSourceIndexes, listSources } from "./api.js";

// listingTTL bounds how long the index listing is reused. Which sources a
// game maps changes only through Setup, and a stale answer here costs at
// most one missing or one extra notice.
const listingTTL = 60_000;
let listing = null; // { at, promise }

// built remembers the (source, game) pairs already seen present: an index
// does not go cold again during a session except through a prune, and a
// search must not re-ask about one on every keystroke's Enter.
const built = new Set();

function indexListing() {
  const now = Date.now();
  if (!listing || now - listing.at > listingTTL) {
    listing = {
      at: now,
      promise: listSourceIndexes().catch(() => ({ indexes: [] })),
    };
  }
  return listing.promise;
}

/** forgetIndexListing drops the cached listing - the Setup page calls it
 * after it has built, refreshed or pruned an index. */
export function forgetIndexListing() {
  listing = null;
  built.clear();
}

/**
 * coldIndexNotice resolves to the sentence to show while a search is
 * pending, or "" when every index it will use is already built. onlySource
 * narrows it to the one source a filtered search asks. Never rejects: a
 * notice is a courtesy, and the search's own result reports a real failure.
 */
export async function coldIndexNotice(game, onlySource) {
  if (!game) return "";
  const { indexes = [] } = await indexListing();
  const sources = [
    ...new Set(
      indexes
        .filter((e) => (e.mapped_by ?? []).includes(game))
        .map((e) => e.source)
        .filter((id) => !onlySource || id === onlySource),
    ),
  ].filter((id) => !built.has(`${id}/${game}`));
  if (sources.length === 0) return "";

  const statuses = await Promise.all(
    sources.map((id) => getSourceIndex(id, game).catch(() => null)),
  );
  const cold = [];
  statuses.forEach((status, i) => {
    if (!status) return;
    if (status.present) built.add(`${sources[i]}/${game}`);
    else cold.push(status);
  });
  if (cold.length === 0) return "";

  const names = await sourceNames();
  return cold
    .map(
      (s) =>
        `Building the ${names.get(s.source) ?? s.source} index for ${s.game} (one-time)…`,
    )
    .join(" ");
}

/** sourceNames maps a source id to its display name, or to nothing when
 * the registry cannot be read (the id is then shown instead). */
async function sourceNames() {
  try {
    const rows = await listSources();
    return new Map(rows.map((r) => [r.id, r.name]));
  } catch {
    return new Map();
  }
}
