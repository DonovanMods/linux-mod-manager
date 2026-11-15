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

/**
 * UnresolvedWarning renders a deploy, switch or apply plan's `unresolved`
 * (core.CollectionIncomplete, issue 660): the profile's collection items
 * left to finish, each named. The plan runs without them, and the game may
 * not behave as the curator intended; Finish collection resolves them.
 * Nothing when there are none.
 */
export function UnresolvedWarning({ unresolved }) {
  const items = unresolved?.items ?? [];
  if (items.length === 0) return null;
  return html`
    <section
      class="plan__section plan__attention"
      data-testid="plan-unresolved"
    >
      <p class="plan__note plan__note--warn">
        ${`Collection incomplete: ${items.length} item${items.length === 1 ? "" : "s"} left to finish, which this leaves out - the game may not behave as the curator intended. Finish collection (on the Profile card) resolves ${items.length === 1 ? "it" : "them"}.`}
      </p>
      <ul class="plan__mods">
        ${items.map(
          (t) =>
            html`<li
              key=${`${t.source_id}:${t.mod_id}`}
              class="plan__mod"
              data-unresolved=${`${t.source_id}:${t.mod_id}`}
            >
              <span class="plan__mod-name"
                >${t.name || `${t.source_id}:${t.mod_id}`}</span
              >
              ${t.remedy && html`<p class="plan__note">${t.remedy}</p>`}
            </li>`,
        )}
      </ul>
    </section>
  `;
}
