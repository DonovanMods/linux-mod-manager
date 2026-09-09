// motion.js - the one JavaScript half of app.css's motion language
// (issue 334, owner demo 1: "mild UI animations for the slide-over and a
// consistent motion language app-wide").
//
// Almost all of that language is CSS: three duration tokens, one curve, and
// a prefers-reduced-motion block that collapses the tokens to zero. Exactly
// one thing cannot be done in the stylesheet - holding an element on screen
// long enough for its EXIT to play, when the thing that closes it is a
// route change Preact answers by unmounting the subtree. That needs a
// timer, and a timer needs to know what the tokens currently say.

// EXIT_MILLIS mirrors app.css's --motion-base. It is a duplicated constant
// rather than a computed getComputedStyle read on purpose: reading it back
// costs a layout flush on every close, and the failure mode of a drift here
// is a panel that unmounts slightly early or late - visible, harmless, and
// nothing like worth a synchronous style recalculation for.
const EXIT_MILLIS = 170;

/**
 * Returns how long an exit animation should be given before the element is
 * torn down - zero when the user has asked for reduced motion, in which
 * case the CSS plays nothing and waiting for it would just make the UI feel
 * slow to exactly the people who asked for less of it.
 *
 * matchMedia is read at CALL time, not cached: a user can change the
 * preference while the page is open, and an application that only honoured
 * it at load would keep animating at them until they reloaded.
 */
export function exitMillis() {
  if (typeof window === "undefined" || !window.matchMedia) return EXIT_MILLIS;
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ? 0
    : EXIT_MILLIS;
}
