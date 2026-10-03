// menuplacement.js - the one rule that keeps a dropdown on screen: it opens
// toward whichever side of its trigger has room. Issue 502.
//
// .picker__menu hangs from its trigger's wrapper (.picker, position:
// relative) and, by default, opens RIGHTWARD from the trigger's left edge.
// The activity tray used to be re-anchored to `right: 0` on the assumption
// that the bell sits at the far right of the top bar - true on Mission
// Control, false on the away header (Setup, search, the full mod page),
// where the same bell is near the LEFT and a 26rem tray hung from its right
// edge ran off the left of the viewport. A position assumption per
// dropdown is the bug; no menu may assume where its trigger sits.
//
// So the placement is measured, once, here, for every .picker__menu. The
// menu prefers its CSS default (start-aligned, rightward); only when that
// would cross the viewport does it try the other side (end-aligned,
// leftward, data-align="end"), and when neither side fits - a window
// narrower than the menu plus the trigger's own offset - it is shifted just
// far enough to sit inside the gutters. A menu that already fits is never
// touched, so nothing that worked moves.

import { useEffect, useLayoutEffect, useRef } from "./render.js";

/** gutterPx reads the menu's --menu-gutter token (app.css) as pixels, so the
 * gap kept to the viewport edge is the stylesheet's one number rather than a
 * second copy of it here. A custom property keeps the unit it was written
 * in, so rem has to be resolved against the root font size. The tooltip
 * (tooltip.js, issue 525) keeps the same kind of gap and reads its own
 * property through the same code. */
export function gutterPx(menu, property = "--menu-gutter") {
  const raw = getComputedStyle(menu).getPropertyValue(property).trim();
  const value = parseFloat(raw);
  if (!Number.isFinite(value)) return 0;
  if (raw.endsWith("rem")) {
    return (
      value * parseFloat(getComputedStyle(document.documentElement).fontSize)
    );
  }
  return value;
}

/**
 * placeMenu positions one open menu inside the viewport. It resets any
 * earlier placement first, so it can be re-run on resize from a clean
 * default. Everything it does is a measurement and an attribute or style
 * write; it returns synchronously, so when it runs in a layout effect the
 * browser never paints the unplaced menu.
 */
export function placeMenu(menu) {
  menu.removeAttribute("data-align");
  menu.style.removeProperty("left");

  // clientWidth, not innerWidth: a vertical scrollbar is not usable room.
  const viewport = document.documentElement.clientWidth;
  const gutter = gutterPx(menu);
  const lo = gutter;
  const hi = viewport - gutter;
  const fits = (r) => r.left >= lo && r.right <= hi;

  const start = menu.getBoundingClientRect();
  if (fits(start)) return;

  menu.setAttribute("data-align", "end");
  const end = menu.getBoundingClientRect();
  if (fits(end)) return;

  // Neither side has room for the whole menu: keep the default side and
  // slide it just inside the nearer gutter. A menu wider than the space
  // (the CSS max-width normally prevents it) is pinned to the left gutter.
  menu.removeAttribute("data-align");
  const anchor = menu.offsetParent;
  if (!(anchor instanceof HTMLElement)) return;
  const anchorLeft = anchor.getBoundingClientRect().left + anchor.clientLeft;
  const wanted = Math.max(lo, Math.min(start.left, hi - start.width));
  menu.style.left = `${wanted - anchorLeft}px`;
}

/**
 * useMenuPlacement returns the ref to put on a .picker__menu element. While
 * active is true it places the menu before paint (a layout effect, so there
 * is no visible jump) and again whenever the window or the menu itself
 * changes size - the tray grows as jobs arrive and rows expand.
 *
 * active is for a menu that is rendered conditionally by its own component
 * (`open && html...`): the effect then runs in the commit that mounts it.
 * A component that only mounts while open passes true.
 */
export function useMenuPlacement(active = true) {
  const ref = useRef(null);

  useLayoutEffect(() => {
    const menu = ref.current;
    if (!active || !menu) return;
    placeMenu(menu);
  }, [active]);

  useEffect(() => {
    const menu = ref.current;
    if (!active || !menu) return;
    const replace = () => placeMenu(menu);
    window.addEventListener("resize", replace);
    // ResizeObserver reports once on observe(), which is a redundant but
    // harmless re-placement of an already placed menu.
    const observer =
      typeof ResizeObserver === "function" ? new ResizeObserver(replace) : null;
    observer?.observe(menu);
    return () => {
      window.removeEventListener("resize", replace);
      observer?.disconnect();
    };
  }, [active]);

  return ref;
}
