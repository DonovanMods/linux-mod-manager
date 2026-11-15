// sourcemapping.js - whether a game maps a source well enough to use it
// (issue 653).
//
// A game's source_ids maps a source's id to the game's identifier on it.
// Where a source's requests name the game - the Steam Workshop's app id,
// NexusMods' domain, CurseForge's game id - core uses the mapping only
// when that identifier is non-empty (game.SourceIDs[x] != ""), so an empty
// value maps nothing usable there. Every place in the SPA that asks "can
// this game use X?" for such a source asks it here, so none of them can
// disagree with core or with each other.
//
// (A directory source is mapped with an empty identifier as a matter of
// course; "which sources does this game list" is the key's presence, and
// is not this question.)

/**
 * isSourceMapped reports whether sourceIDs (a game's source_ids, or a
 * status document's) maps sourceID to a non-empty identifier.
 */
export function isSourceMapped(sourceIDs, sourceID) {
  const value = sourceIDs?.[sourceID];
  return typeof value === "string" && value !== "";
}
