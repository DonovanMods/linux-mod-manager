// relativetime.js - one pure helper: an ISO timestamp as the phrase a
// person actually wants ("4 minutes ago"), for facts whose AGE is the
// point rather than their calendar date.
//
// The Health card's "last verified" (issue 334, core.VerifyResult.checked_at)
// is the first: nobody reading it wants to subtract two clock times to
// find out whether the health they are looking at is minutes or days old.
// modrows.js's formatDate stays what it is - the library's "Installed"
// column is a date, not an age, and "347 days ago" would be a worse answer
// there than "12 Sep 2025".

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * Renders value (an RFC 3339 / ISO timestamp) as an age relative to now,
 * or "" when there is nothing parsable to render.
 *
 * Empty rather than a dash, unlike formatDate: this phrase is always part
 * of a sentence a caller composes ("Last verified …"), and half a sentence
 * is worse than no sentence - so a caller checks for "" and omits the whole
 * line instead.
 *
 * A timestamp slightly in the FUTURE reads as "just now" rather than as a
 * negative age. That is a real case, not a defensive one: the server stamps
 * checked_at from its own clock and the browser compares against its own,
 * and the two need only disagree by a second.
 */
export function relativeTime(value, now = Date.now()) {
  const ms = Date.parse(value);
  if (Number.isNaN(ms)) return "";

  const elapsed = now - ms;
  if (elapsed < MINUTE) return "just now";
  if (elapsed < HOUR) return plural(Math.floor(elapsed / MINUTE), "minute");
  if (elapsed < DAY) return plural(Math.floor(elapsed / HOUR), "hour");
  if (elapsed < 7 * DAY) return plural(Math.floor(elapsed / DAY), "day");

  // Past a week an age stops being useful and a date starts being: "23
  // days ago" is a number to decode, "12 Sep" is a fact.
  return `on ${new Date(ms).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  })}`;
}

/** plural is the "N units ago" phrase, built as ONE string - htm collapses
 * whitespace between adjacent interpolations, so a caller that assembled
 * this in a template would silently fuse the number to the unit. */
function plural(count, unit) {
  return `${count} ${unit}${count === 1 ? "" : "s"} ago`;
}
