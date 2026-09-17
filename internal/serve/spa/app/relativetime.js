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
  return pastPhrase(now - ms, ms);
}

/**
 * countdown renders value as the wait until it - "in 9 minutes" - for a
 * moment deliberately ahead of now, such as a source.Hold's retry_at
 * (sourceindexes.js, issue 436). "" once it has passed or is unparsable.
 *
 * Its own function rather than a future branch of relativeTime: a
 * timestamp a second ahead there is clock skew between server and browser
 * and reads "just now", while a hold a second ahead is still a wait.
 */
export function countdown(value, now = Date.now()) {
  const ms = Date.parse(value);
  if (Number.isNaN(ms) || ms <= now) return "";
  return futurePhrase(ms - now);
}

/** pastPhrase is relativeTime's phrase for an age; a slightly negative one
 * (clock skew) is "just now". */
function pastPhrase(elapsed, ms) {
  if (elapsed < MINUTE) return "just now";
  if (elapsed < HOUR)
    return plural(Math.floor(elapsed / MINUTE), "minute", "ago");
  if (elapsed < DAY) return plural(Math.floor(elapsed / HOUR), "hour", "ago");
  if (elapsed < 7 * DAY) return plural(Math.floor(elapsed / DAY), "day", "ago");

  // Past a week an age stops being useful and a date starts being: "23
  // days ago" is a number to decode, "12 Sep" is a fact.
  return `on ${new Date(ms).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  })}`;
}

/** futurePhrase is a countdown to a moment not yet reached. */
function futurePhrase(remaining) {
  if (remaining < MINUTE) return "any moment now";
  if (remaining < HOUR)
    return plural(Math.floor(remaining / MINUTE), "minute", "in");
  if (remaining < DAY)
    return plural(Math.floor(remaining / HOUR), "hour", "in");
  return plural(Math.floor(remaining / DAY), "day", "in");
}

/** plural is the "N units ago"/"in N units" phrase, built as ONE string -
 * htm collapses whitespace between adjacent interpolations, so a caller
 * that assembled this in a template would silently fuse the number to the
 * unit. */
function plural(count, unit, dir) {
  const phrase = `${count} ${unit}${count === 1 ? "" : "s"}`;
  return dir === "in" ? `in ${phrase}` : `${phrase} ago`;
}
