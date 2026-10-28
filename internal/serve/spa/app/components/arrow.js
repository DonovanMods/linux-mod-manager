// arrow.js - the one arrow every navigation control draws (issue 520).
//
// "←" and "→" as text come from whatever font the system falls back to,
// since the UI's own type rarely carries them, and that font sits its arrows
// below the label's centre line: Prev and Next looked a pixel or two low
// beside their words. An inline SVG is laid out in the label's own box, so
// the flex centring every control already has centres it exactly, in every
// font. It is drawn in currentColor, so it takes the control's own colour,
// disabled and hover states included.
//
// Decorative: the label beside it (or the control's aria-label) says where
// the control goes, so the icon is hidden from assistive technology.

import { html } from "../render.js";

const PATHS = {
  left: "M13 8H3M7 4 3 8l4 4",
  right: "M3 8h10M9 4l4 4-4 4",
};

/** Arrow draws a left or right arrow, 1em square. */
export function Arrow({ dir }) {
  return html`<svg
    class="arrow"
    viewBox="0 0 16 16"
    aria-hidden="true"
    focusable="false"
  >
    <path
      d=${PATHS[dir]}
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  </svg>`;
}
