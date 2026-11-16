// planhandoffs.js - the shared files a plan's removals pass to the next
// mod in load order (issue 638, over issue 633's hand-off): a plan's
// `handoffs`, as core.HandOff lists them, one row per mod that takes a
// path - its paths, and the mods they come from. It groups and caps
// exactly as the CLI's preview does (cmd/lmm/handoffs.go): five paths per
// mod before "and N more", ten mods before one row counts the rest.

import { html } from "../render.js";
import { deployedPath } from "../deploypath.js";

/** PATH_LIMIT is how many paths one mod's row names. */
export const PATH_LIMIT = 5;
/** HEIR_LIMIT is how many mods get a row of their own. */
export const HEIR_LIMIT = 10;

/** handOffGroups groups handoffs by the mod each passes to, in the order
 * each mod first appears: [{key, heir, paths, from}]. */
export function handOffGroups(handoffs) {
  const order = [];
  const groups = new Map();
  for (const h of handoffs ?? []) {
    const key = `${h.to.source_id}:${h.to.mod_id}`;
    let g = groups.get(key);
    if (!g) {
      g = { key, heir: h.to.name, paths: [], from: [] };
      groups.set(key, g);
      order.push(g);
    }
    g.paths.push(deployedPath(h.path, h.root));
    if (!g.from.includes(h.from.name)) g.from.push(h.from.name);
  }
  return order;
}

/** pathList is a group's paths, capped at PATH_LIMIT. */
function pathList(paths) {
  const shown = paths.slice(0, PATH_LIMIT).join(", ");
  const extra = paths.length - PATH_LIMIT;
  return extra > 0 ? `${shown} and ${extra} more` : shown;
}

/** PlanHandOffs renders a plan's `handoffs`, or nothing when empty;
 * heading, when given, replaces the section's own. */
export function PlanHandOffs({
  handoffs,
  heading = "Shared files passing to the next mod in load order",
  testID = "plan-handoffs",
}) {
  const list = handoffs ?? [];
  if (list.length === 0) return null;
  const groups = handOffGroups(list);
  const shown = groups.slice(0, HEIR_LIMIT);
  const rest = groups.slice(HEIR_LIMIT);
  const restFiles = rest.reduce((n, g) => n + g.paths.length, 0);
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">${heading} (${list.length})</h3>
      <ul class="plan__mods" data-testid=${testID}>
        ${shown.map(
          (g) => html`
            <li key=${g.key} class="plan__mod" data-heir=${g.key}>
              <span class="mono">${pathList(g.paths)}</span>${" "}
              <span class="plan__mod-detail"
                >${g.paths.length === 1 ? "passes" : "pass"} to${" "}</span
              ><span class="plan__mod-name">${g.heir}</span>${" "}
              <span class="plan__mod-detail">(from ${g.from.join(", ")})</span>
            </li>
          `,
        )}
        ${
          rest.length > 0 &&
          html`<li key="more" class="plan__mod">
            <span class="plan__mod-detail"
              >and ${rest.length} more mod${rest.length === 1 ? "" : "s"} take
              ${restFiles} more file${restFiles === 1 ? "" : "s"}</span
            >
          </li>`
        }
      </ul>
    </section>
  `;
}
