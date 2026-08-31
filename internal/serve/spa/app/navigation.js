// navigation.js - resolving a game to the Mission Control URL it opens on.
//
// The game chooser's cards and the top bar's game picker both need this: a
// game alone is not a route, /g/{game}/{profile} is, and the profile half is
// "whichever one is active" - the same profile > game > global resolution
// `lmm status --game <id>` already performs, so it belongs to one call
// rather than being re-derived from a profile list on each caller's own.

import { get } from "./api.js";
import { contextPath } from "./router.js";

/**
 * Resolves gameID's Mission Control path via its GameStatus, or null when
 * the game has no active profile to land on yet (a freshly added game with
 * no profile created) - the caller's cue to stay where it is rather than
 * navigate into a context that doesn't resolve.
 */
export async function resolveGamePath(gameID) {
  const status = await get(
    `/api/v1/status?${new URLSearchParams({ game: gameID })}`,
  );
  if (!status.active_profile) return null;
  return contextPath(gameID, status.active_profile);
}
