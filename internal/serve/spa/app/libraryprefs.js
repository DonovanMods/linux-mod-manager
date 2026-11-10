// libraryprefs.js - the library's per-viewer view preferences (issue 592):
// whether the table is grouped by category, and which groups are collapsed.
//
// Remembered in localStorage like the theme (theme.js), and for the same
// reason: they are how THIS viewer likes to look at the table, not state the
// profile or another browser should see. Every read and write is wrapped -
// a private window or disabled storage throws - and an unreadable
// preference is simply the default: ungrouped, nothing collapsed.

const GROUP_KEY = "lmm-library-group-by-category";
const COLLAPSED_KEY_PREFIX = "lmm-library-collapsed-groups:";

/** Whether the viewer last chose to group the library by category. */
export function readGroupByCategory() {
  try {
    return localStorage.getItem(GROUP_KEY) === "true";
  } catch {
    return false;
  }
}

/** Remembers the grouping choice; off removes the key rather than storing
 * a second value, so the default needs nothing stored. */
export function writeGroupByCategory(on) {
  try {
    if (on) localStorage.setItem(GROUP_KEY, "true");
    else localStorage.removeItem(GROUP_KEY);
  } catch {
    // Not remembered; the choice still holds for this page.
  }
}

/** The names of the category groups the viewer collapsed in gameID's
 * library, as a Set. Per game, because category names are: "UI" in one
 * game's library says nothing about another's. */
export function readCollapsedGroups(gameID) {
  try {
    const raw = JSON.parse(
      localStorage.getItem(COLLAPSED_KEY_PREFIX + gameID) ?? "[]",
    );
    return new Set(
      Array.isArray(raw) ? raw.filter((n) => typeof n === "string") : [],
    );
  } catch {
    return new Set();
  }
}

/** Remembers gameID's collapsed groups (a Set of names). */
export function writeCollapsedGroups(gameID, names) {
  try {
    if (names.size === 0) {
      localStorage.removeItem(COLLAPSED_KEY_PREFIX + gameID);
    } else {
      localStorage.setItem(
        COLLAPSED_KEY_PREFIX + gameID,
        JSON.stringify([...names]),
      );
    }
  } catch {
    // Not remembered; the collapse still holds for this page.
  }
}
