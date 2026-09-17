// planwarnings.js - a plan document's own `warnings`, rendered before
// Confirm (issue 463). Core writes each for both frontends, so each is
// shown verbatim, commands set in the monospace face (errortext.js).

import { html } from "../render.js";
import { codeSpans } from "../errortext.js";

/** PlanWarnings renders a plan's warnings list, or nothing when empty. */
export function PlanWarnings({ warnings, testID = "plan-warnings" }) {
  const list = (warnings ?? []).filter((w) => typeof w === "string" && w);
  if (list.length === 0) return null;
  return html`
    <ul class="plan__warnings" data-testid=${testID}>
      ${list.map(
        (w, i) =>
          html`<li key=${i} class="plan__note plan__note--warn">
            ${codeSpans(w)}
          </li>`,
      )}
    </ul>
  `;
}
