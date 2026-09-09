// documentview.js - a readable rendering of an arbitrary /api/v1 document.
//
// It exists for the two places this application must show something it has
// no bespoke renderer for:
//
//   1. a plan kind with no registered renderer yet (planrenderers.js's
//      fallback) - a later unit's mutation, previewed honestly today
//      instead of as an empty modal;
//   2. a failed job's `details` payload, which is whatever typed extension
//      the core error carried (a conflict's file list, a stale plan's
//      snapshot diff, …). The tray must show that rather than swallow it.
//
// The rule it follows is the one both cases need: show everything the
// document actually carries, invent nothing, and never claim a value is
// absent when it is merely nested. It is deliberately plain - a bespoke
// renderer always reads better, which is why the registry prefers one.

import { html } from "../render.js";

// maxDepth bounds the recursion. Nothing on this wire nests anywhere near
// this deep; the cap exists so a cyclic or pathological document degrades
// into a visible "…" instead of hanging the render loop.
const maxDepth = 5;

/** Turns a wire member name into a heading: "total_bytes" -> "Total bytes". */
export function humanizeKey(key) {
  const words = String(key).replaceAll("_", " ").trim();
  return words ? words[0].toUpperCase() + words.slice(1) : words;
}

/**
 * Renders one JSON value. Objects become a definition list, arrays a list,
 * and every scalar its own plain text - with `false` and `0` rendered
 * rather than dropped, since "the document says zero" and "the document
 * says nothing" are different facts.
 */
export function DocumentView({ value, depth = 0 }) {
  if (value === null || value === undefined) {
    return html`<span class="doc__empty">—</span>`;
  }
  if (typeof value === "boolean") {
    return html`<span class="doc__scalar">${value ? "yes" : "no"}</span>`;
  }
  if (typeof value !== "object") {
    return html`<span class="doc__scalar">${String(value)}</span>`;
  }
  if (depth >= maxDepth) {
    return html`<span class="doc__empty">…</span>`;
  }

  if (Array.isArray(value)) {
    if (value.length === 0) return html`<span class="doc__empty">none</span>`;
    return html`
      <ul class="doc__list">
        ${value.map(
          (item, i) => html`
            <li key=${i}>
              <${DocumentView} value=${item} depth=${depth + 1} />
            </li>
          `,
        )}
      </ul>
    `;
  }

  const entries = Object.entries(value);
  if (entries.length === 0) return html`<span class="doc__empty">none</span>`;
  return html`
    <dl class="doc__pairs">
      ${entries.map(
        ([key, member]) => html`
          <div class="doc__pair" key=${key}>
            <dt class="doc__key">${humanizeKey(key)}</dt>
            <dd class="doc__value">
              <${DocumentView} value=${member} depth=${depth + 1} />
            </dd>
          </div>
        `,
      )}
    </dl>
  `;
}
