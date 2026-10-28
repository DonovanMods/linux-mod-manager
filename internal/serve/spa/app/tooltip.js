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
// replaces needs nothing re-attached. Anything that moves the trigger
// (scroll, resize) or acts on it (click) hides the tooltip rather than
// leaving it pointing at the wrong place.

import { gutterPx } from "./menuplacement.js";

const GAP_PX = 6;

let tip = null;
let trigger = null;

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

function show(anchor) {
  const text = anchor.getAttribute("data-tooltip");
  if (!text) return hide();
  const el = element();
  trigger = anchor;
  el.textContent = text;
  el.dataset.visible = "true";
  placeTooltip(el, anchor);
}

function hide() {
  trigger = null;
  if (tip) tip.dataset.visible = "false";
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
      if (anchor) show(anchor);
      else if (trigger) hide();
    }),
    on("mouseout", (event) => {
      if (trigger && !trigger.contains(event.relatedTarget)) hide();
    }),
    on("focusin", (event) => {
      const anchor = owner(event.target);
      // :focus-visible is what separates a keyboard arrival from a click,
      // which already has the pointer's own hover to show it.
      if (anchor?.matches(":focus-visible")) show(anchor);
    }),
    on("focusout", () => hide()),
    on("keydown", (event) => {
      if (event.key === "Escape" && trigger) hide();
    }),
    on("click", () => hide()),
    on("scroll", () => hide(), true),
  ];
  window.addEventListener("resize", hide);
  return () => {
    for (const off of offs) off();
    window.removeEventListener("resize", hide);
    tip?.remove();
    tip = null;
    trigger = null;
  };
}
