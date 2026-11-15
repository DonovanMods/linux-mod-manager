// livetodos.js - how many items each collection profile has left to finish,
// as the page last read it (issue 660: state.liveTodos, "game:profile" ->
// count, written by main.js#noteTodos). It is what lets a finished
// import's activity entry stop asking for attention once its collection
// is finished - wherever it was finished, and whoever started the job.

import { ApiError, getProfileTodos } from "./api.js";

/** liveTodosKey is a collection profile's key in state.liveTodos. */
export function liveTodosKey(game, profile) {
  return `${game ?? ""}:${profile ?? ""}`;
}

/**
 * hydratedTodos is the to-do list a hydrate of route read (report), when it
 * still counts for route: the page is still on route's game and profile
 * (current, the live route) and the list is that profile's. A newer
 * hydrate of another game - with a profile of the same name - may have
 * replaced the list by the time an older one ends, and its count must not
 * be written under the older one's game. Null when it does not count.
 */
export function hydratedTodos(route, current, report) {
  if (!report || !route?.game || !route?.profile) return null;
  if (current?.game !== route.game || current?.profile !== route.profile) {
    return null;
  }
  return report.profile === route.profile ? report : null;
}

// inFlight is each list's read under way, by liveTodosKey - {done, notes}:
// one read of a list at a time, however many entries ask for it, and every
// caller's note hears its answer (one that joins mid-flight included).
const inFlight = new Map();

/**
 * refreshLiveTodos reads profile's to-do list in game and hands it to note
 * (main.js#noteTodos). A 404 - the profile was deleted or renamed - leaves
 * nothing to finish, and is noted as an empty list; any other failure
 * notes nothing, so the last count stands. read is the fetch (the API by
 * default; a test hands its own). Resolves when the read has settled.
 */
export function refreshLiveTodos(game, profile, note, read = getProfileTodos) {
  const key = liveTodosKey(game, profile);
  const pending = inFlight.get(key);
  if (pending) {
    pending.notes.push(note);
    return pending.done;
  }
  const notes = [note];
  const tell = (doc) => {
    for (const each of notes) each(game, doc);
  };
  const done = Promise.resolve()
    .then(() => read(profile, { game }))
    .then(tell, (err) => {
      if (err instanceof ApiError && err.status === 404) {
        tell({ profile, todos: [] });
      }
    })
    .finally(() => inFlight.delete(key));
  inFlight.set(key, { done, notes });
  return done;
}
