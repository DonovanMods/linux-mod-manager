// sourcenames.js - a source's display name, for the places that name one
// ("Open on Nexus Mods").
//
// No document a mod is shown from carries its source's name - only its id -
// and GET /api/v1/sources is the one place that does. It is read once per
// page load and shared: every link on a screen of fifty search results asks
// for the same answer.

import { useEffect, useState } from "./render.js";
import { listSources } from "./api.js";

/** names is the settled id -> display name map, or null before the first
 * read lands. A failed read settles it to an empty map: a link then names
 * the source by its id, which is true if less pretty, and is never retried
 * per link. */
let names = null;
let pending = null;

function load() {
  if (!pending) {
    pending = listSources().then(
      (rows) => {
        names = new Map(
          (Array.isArray(rows) ? rows : [])
            .filter((r) => r && r.id && r.name)
            .map((r) => [r.id, r.name]),
        );
      },
      () => {
        names = new Map();
      },
    );
  }
  return pending;
}

/**
 * useSourceName returns sourceID's display name, or sourceID itself until the
 * name is known (and for good when the source list cannot be read). `enabled`
 * false (a caller with nothing to name) reads nothing: the list is fetched
 * only once something on the page actually shows a name.
 */
export function useSourceName(sourceID, enabled = true) {
  const [, setLoaded] = useState(names !== null);
  useEffect(() => {
    if (!enabled || names !== null) return;
    let cancelled = false;
    load().then(() => {
      if (!cancelled) setLoaded(true);
    });
    return () => {
      cancelled = true;
    };
  }, [enabled]);
  return names?.get(sourceID) ?? sourceID;
}
