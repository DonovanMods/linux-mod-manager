// version.js - the one place this SPA turns a mod's `version` wire field
// into text a human reads.
//
// It exists because of issue 269's approval note (docs/plans/2026-09-09-steam-
// workshop-design.md, "Approved with notes"): an EXTERNAL mod's
// domain.Mod.Version holds Steam's 19-digit content id - the item's version
// IDENTITY, and not a version anybody can read - so "no human-facing surface
// prints a 19-digit number as a version". The surface shows the item's
// revision date instead, and only `lmm mod show` and the mod page carry the
// manifest at all, labelled as such underneath.
//
// That rule was first implemented as a derived field on one library row, and
// five sibling surfaces went on printing the raw field: the mod panel's meta
// line, the full mod page's, the uninstall confirmation, the Updates card and
// its confirm modal. Each of them had a perfectly good reason not to have the
// derived field - a different document, a plan rather than a listing, its own
// row literal - which is exactly why the rule needs a function every surface
// calls rather than a value one document happens to carry.
//
// internal/serve/version_display_test.go is the ratchet: every `.version`,
// `.new_version`, `.installed_version` and `.available_version` read anywhere
// under spa/app must either be one of the two functions below or be listed
// there with a reason. A new surface that reads the field raw fails the build.

/**
 * isoDate renders a wire timestamp as a plain YYYY-MM-DD date, or "" when
 * there is none. Deliberately not exported: a date in the slot a VERSION goes
 * is this module's business, and every other date on screen is somebody
 * else's (modrows.js#formatDate for the library's "Installed" column,
 * relativetime.js for the activity surfaces).
 */
function isoDate(value) {
  if (!value) return "";
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? "" : new Date(ms).toISOString().slice(0, 10);
}

/**
 * displayVersion is the version text for one mod - a core.ModListing, a
 * domain.InstalledMod, or anything else carrying the same two wire fields.
 *
 * For an EXTERNAL mod it is the item's revision date (domain.Mod.UpdatedAt,
 * which the Workshop source stamps from Steam's own time_updated), falling
 * back to an em dash when the row carries none: a blank cell under a heading
 * reads as a rendering bug, while "—" says the document has no date, which is
 * a real state. For every other mod it is the version verbatim, which is what
 * every surface did before this rule existed.
 */
export function displayVersion(mod) {
  if (!mod) return "";
  if (mod.external) return isoDate(mod.updated_at) || "—";
  return mod.version ?? "";
}

/**
 * displayUpdateTarget is displayVersion's other half: the text for the version
 * a domain.Update would move the mod TO.
 *
 * A Workshop update's target is another 19-digit content id, and lmm has no
 * date for a revision it has not seen, so "newer" is the whole of what it can
 * truthfully say about it - which is also all a user can act on, since Steam
 * applies the update itself either way.
 */
export function displayUpdateTarget(update) {
  if (!update) return "";
  if (update.installed_mod?.external) return "newer";
  return update.new_version ?? "";
}
