// searchtype.js - what the search page searches (issue 651): the game's mods
// (the default) or its collections. The route carries it as ?type=, so a
// link, a reload and the omnibar's "Search collections" keep it.

/** MODS is the mod search, and the default: the route carries no ?type=. */
export const MODS = "mods";

/** COLLECTIONS is the collection search: GET /api/v1/search/collections,
 * `lmm search --collections`. */
export const COLLECTIONS = "collections";

/** COLLECTION_SOURCES are the built-in sources whose collections can be
 * searched: a client-side list, as the search page's tag-capable sources
 * are, since nothing on the wire advertises the capability. A game mapping
 * none of them gets no Collections tab. */
export const COLLECTION_SOURCES = new Set(["nexusmods"]);

/** knownSearchType reads a route's raw ?type=: COLLECTIONS when it says so,
 * MODS for anything else. */
export function knownSearchType(raw) {
  return raw === COLLECTIONS ? COLLECTIONS : MODS;
}

/** collectionsSearchable is whether the game state.status describes maps a
 * source whose collections can be searched. */
export function collectionsSearchable(status) {
  return Object.entries(status?.source_ids ?? {}).some(
    ([id, gameSourceID]) => COLLECTION_SOURCES.has(id) && Boolean(gameSourceID),
  );
}

/** searchPath is the search route for query under home (a game/profile
 * context path), searching type: the omnibar's links and the import
 * dialog's "Browse collections" build it. */
export function searchPath(home, query, type = MODS) {
  const params = new URLSearchParams();
  if (query) params.set("q", query);
  if (type === COLLECTIONS) params.set("type", COLLECTIONS);
  const qs = params.toString();
  return `${home}/search${qs ? `?${qs}` : ""}`;
}
