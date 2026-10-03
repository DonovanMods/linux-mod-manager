// pencil.js - the one pencil every "edit this" control draws (issue 525).
//
// A table row that repeats "Edit mod path…", "Edit sources…" and "Edit
// loader…" beside every value duplicates its own column headers and crowds
// the row, so each edit is a pencil icon button beside its value (the
// control system's `--quiet --icon` shape) with the field and the row in its
// aria-label and the field in its tooltip (tooltip.js).
//
// Inline SVG, like arrow.js: no icon font, no asset. It is laid out in the
// control's own box, so the flex centring every control has centres it in
// every font, and it is drawn in currentColor, so it takes the control's own
// colour - disabled and hover states included.
//
// Decorative: the control's aria-label is its name, so the icon is hidden
// from assistive technology.

import { html } from "../render.js";

/** Pencil draws a pencil, 1em square. */
export function Pencil() {
  return html`<svg
    class="pencil"
    viewBox="0 0 16 16"
    aria-hidden="true"
    focusable="false"
  >
    <path
      d="M10.5 2.5l3 3L5.5 13.5 2 14l.5-3.5zM9 4l3 3"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  </svg>`;
}

/** EditButton is a table row's pencil: a quiet, small icon button whose
 * accessible name is `label` ("Edit mod path for Skyrim") and whose tooltip
 * is `tip` ("Edit mod path"). `expanded`, when the pencil opens an editor
 * below its row, is announced as aria-expanded instead of by a changed
 * label, so the name stays the same while the editor is open. Everything
 * else (data-action, data-game, disabled, onClick) is passed through. */
export function EditButton({ label, tip, expanded, ...rest }) {
  return html`<button
    type="button"
    class="button button--quiet button--icon button--small"
    aria-label=${label}
    data-tooltip=${tip}
    aria-expanded=${expanded === undefined ? undefined : expanded ? "true" : "false"}
    ...${rest}
  >
    <${Pencil} />
  </button>`;
}
