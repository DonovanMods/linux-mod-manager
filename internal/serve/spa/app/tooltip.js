// tooltip.js - the one tooltip every control with a data-tooltip draws
// (issue 525).
//
//   <button aria-label="Edit mod path for Skyrim" data-tooltip="Edit mod path">
//
// It shows on hover AND on keyboard focus (hover alone would exclude
// keyboard and touch users), and Escape dismisses it without moving focus
// (WCAG 1.4.13). It is NOT how the control is named: the tooltip element is
// aria-hidden and has no role, so a screen reader hears the control's own
// aria-label once and never the same words again; the tooltip is for the
// people who can see it.
//
// One element, shared by every trigger, appended to <body> and positioned
// fixed from the trigger's measured box. That is what keeps it from being
// clipped by a table cell or a scrolling container, and the measurement is
// menuplacement.js's rule applied to a tooltip: it prefers below the
// trigger, centred; it flips above when the bottom edge has no room; and it
// is slid just inside the viewport gutters when its natural spot would cross
// one, so the right-most column's tooltip never runs off the page.
//
// Events are delegated from the document, so a trigger the render loop
// replaces needs nothing re-attached. Anything that acts on the trigger
// (click) or moves it (resize, and a scroll under a hovered one) hides the
// tooltip rather than leaving it pointing at the wrong place.
//
// A tooltip keyboard focus raised belongs to the focus, not the pointer
// (issue 572). Focusing a control the page has to scroll to - a narrow
// window's right-hand column - scrolls it into view, and that scroll's
// event arrives a frame AFTER the focusin that showed the tooltip; hiding
// on it took the tooltip down the moment it went up. So a scroll re-places a focus-raised
// tooltip beside its trigger (hiding it only once the trigger has left the
// viewport), and the pointer events that same scroll provokes - the browser
// re-targets a pointer the content moved under - leave it alone. Blur,
// Escape and a click still dismiss it.

import { gutterPx } from "./menuplacement.js";

const GAP_PX = 6;

let tip = null;
let trigger = null;
// via is what raised the tooltip: "pointer" or "focus".
let via = null;

function element() {
  if (!tip) {
    tip = document.createElement("div");
    tip.className = "tooltip";
    tip.setAttribute("aria-hidden", "true");
    document.body.append(tip);
  }
  return tip;
}

/** placeTooltip puts the (already filled) tooltip beside its trigger and
 * inside the viewport. */
function placeTooltip(el, anchor) {
  el.style.left = "0px";
  el.style.top = "0px";
  const viewW = document.documentElement.clientWidth;
  const viewH = window.innerHeight;
  const gutter = gutterPx(el, "--tooltip-gutter");
  const a = anchor.getBoundingClientRect();
  const box = el.getBoundingClientRect();

  const left = Math.max(
    gutter,
    Math.min(a.left + a.width / 2 - box.width / 2, viewW - gutter - box.width),
  );
  let top = a.bottom + GAP_PX;
  if (top + box.height > viewH - gutter) {
    top = a.top - GAP_PX - box.height;
  }
  top = Math.max(gutter, top);
  el.style.left = `${left}px`;
  el.style.top = `${top}px`;
}

function show(anchor, how) {
  const text = anchor.getAttribute("data-tooltip");
  if (!text) return hide();
  const el = element();
  trigger = anchor;
  via = how;
  el.textContent = text;
  el.dataset.visible = "true";
  placeTooltip(el, anchor);
}

function hide() {
  trigger = null;
  via = null;
  if (tip) tip.dataset.visible = "false";
}

/** onScroll follows a focus-raised tooltip's trigger and hides a hovered
 * one's. */
function onScroll() {
  if (via !== "focus") return hide();
  const a = trigger.getBoundingClientRect();
  const viewW = document.documentElement.clientWidth;
  if (
    a.bottom <= 0 ||
    a.top >= window.innerHeight ||
    a.right <= 0 ||
    a.left >= viewW
  ) {
    return hide();
  }
  placeTooltip(tip, trigger);
}

/** installTooltips wires the delegated listeners once. It returns a function
 * that removes them (for a test; the page keeps them for its lifetime). */
export function installTooltips() {
  const on = (type, handler, options) => {
    document.addEventListener(type, handler, options);
    return () => document.removeEventListener(type, handler, options);
  };
  const owner = (node) =>
    node instanceof Element ? node.closest("[data-tooltip]") : null;

  const offs = [
    on("mouseover", (event) => {
      const anchor = owner(event.target);
      if (anchor) show(anchor, "pointer");
      else if (via === "pointer") hide();
    }),
    on("mouseout", (event) => {
      if (via === "pointer" && !trigger.contains(event.relatedTarget)) hide();
    }),
    on("focusin", (event) => {
      const anchor = owner(event.target);
      // :focus-visible is what separates a keyboard arrival from a click,
      // which already has the pointer's own hover to show it.
      if (anchor?.matches(":focus-visible")) show(anchor, "focus");
    }),
    on("focusout", () => hide()),
    on("keydown", (event) => {
      if (event.key === "Escape" && trigger) hide();
    }),
    on("click", () => hide()),
    on("scroll", onScroll, true),
  ];
  window.addEventListener("resize", hide);
  return () => {
    for (const off of offs) off();
    window.removeEventListener("resize", hide);
    tip?.remove();
    tip = null;
    trigger = null;
    via = null;
  };
}
