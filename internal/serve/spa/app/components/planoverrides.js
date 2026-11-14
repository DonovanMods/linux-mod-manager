// planoverrides.js - a switch or apply plan's override changes (issue 631):
// every file the profile's overrides change, as core.OverrideChange lists
// them. A game file is named under "<game root>/", the way the CLI names it;
// a mod's own file (issue 630) by its deployed path, with the mod it belongs
// to.

import { html } from "../render.js";

/** ACTIONS words each core.OverrideChangeAction for a person. */
const ACTIONS = {
  place: "write the override",
  update: "rewrite the override",
  restore: "take the override away",
};

/** changePath is a change's path as the CLI prints it. */
function changePath(change) {
  return change.root === "install_path"
    ? `<game root>/${change.path}`
    : change.path;
}

/** PlanOverrides renders a plan's `overrides`, or nothing when empty. */
export function PlanOverrides({ changes, testID }) {
  const list = changes ?? [];
  if (list.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">Profile overrides (${list.length})</h3>
      <ul class="plan__paths" data-testid=${testID}>
        ${list.map(
          (c) => html`
            <li
              key=${`${c.root ?? ""}:${c.path}`}
              class="plan__mod"
              data-action=${c.action}
            >
              <span class="mono">${changePath(c)}</span>${" "}
              <span class="plan__mod-detail"
                >${ACTIONS[c.action] ?? c.action}${
                  c.mod_id ? `, over ${c.source_id}:${c.mod_id}'s own file` : ""
                }</span
              >
            </li>
          `,
        )}
      </ul>
    </section>
  `;
}
