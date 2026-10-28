// focustrap.js - keyboard containment for the two surfaces that put a
// scrim over the page: the confirm/reorder/profiles modal (modal.js) and
// the slide-over (modpanel.js). Issue 334's a11y pass; modal.js's own
// comment has promised it since issue 332 ("there is no focus trap ... full
// keyboard containment is Unit 8's a11y pass").
//
// Why it is needed at all: both surfaces darken the page behind them and
// close on Escape, which tells a sighted user that the page is out of
// reach. Nothing tells the KEYBOARD that. Focusing the panel on open (which
// both already did) only fixes where Tab STARTS - a few more presses and
// the cursor is walking a library table nobody can see, clicking things
// that are behind a scrim.
//
// A trap rather than `inert` on the rest of the page: `inert` would be the
// tidier mechanism, but it has to be applied to every sibling of the
// overlay, and this application mounts its overlays as siblings of whatever
// screen is up (components/app.js) - so "every sibling" changes per route.
// Cycling within the container needs nothing outside it.

// FOCUSABLE is the set of elements the browser will hand focus to on Tab.
// Deliberately not exhaustive (no [contenteditable], no <audio controls>) -
// this application has none of those, and a selector that lies about what
// it covers is worse than one that names its scope.
// Every entry excludes tabindex="-1" explicitly: an element opted OUT of
// the tab order that way is still a <button>, and a trap that cycled onto
// one would put the cursor somewhere the browser itself never would - which
// is exactly what the setup page's roving-tabindex tab strip (four of its
// five tabs carry -1 at any moment) would have made happen.
const FOCUSABLE = [
  'a[href]:not([tabindex="-1"])',
  'button:not([disabled]):not([tabindex="-1"])',
  'input:not([disabled]):not([tabindex="-1"])',
  'select:not([disabled]):not([tabindex="-1"])',
  'textarea:not([disabled]):not([tabindex="-1"])',
  '[tabindex]:not([tabindex="-1"])',
].join(", ");

/**
 * Cycles Tab and Shift+Tab within container, and returns the cleanup - the
 * shape a Preact effect wants.
 *
 * getContainer is a function rather than the element itself because a
 * caller holds its panel in a ref, which is null on the first render pass
 * the effect is registered from.
 *
 * The listener is registered in the CAPTURE phase so it sees Tab before any
 * control inside the panel that handles keys of its own (the setup page's
 * tab strip, the reorder list's drag alternatives). It only ever acts on
 * Tab, so nothing else is disturbed.
 */
export function trapFocus(getContainer) {
  function onKeyDown(e) {
    if (e.key !== "Tab") return;
    const container = getContainer();
    if (!container) return;

    const items = focusableWithin(container);
    if (items.length === 0) {
      // Nothing inside to focus: keep the cursor on the panel itself
      // rather than letting it escape to the page behind the scrim.
      e.preventDefault();
      container.focus();
      return;
    }

    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    const inside = container.contains(active);

    if (e.shiftKey) {
      if (!inside || active === first) {
        e.preventDefault();
        last.focus();
      }
      return;
    }
    if (!inside || active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  document.addEventListener("keydown", onKeyDown, true);
  return () => document.removeEventListener("keydown", onKeyDown, true);
}

/** focusableWithin lists container's focusable descendants in DOM order,
 * skipping the ones that are not rendered. getClientRects() rather than
 * offsetParent: an overlay is position:fixed, and a fixed element's
 * offsetParent is null even when it is plainly on screen. */
function focusableWithin(container) {
  return [...container.querySelectorAll(FOCUSABLE)].filter(
    (el) => el.getClientRects().length > 0,
  );
}
