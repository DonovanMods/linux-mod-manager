// plan_mod_fomod.js - the confirm modal's renderer for a
// core.FomodReconfigurePlan (kind_mod_fomod.go, `lmm mod fomod`, issue
// 615): "Reconfigure installer…" on the full mod page.
//
// There is nothing to choose here yet: Continue fetches the installed
// version again and the installer's questions follow in the wizard
// (fomodwizard.js), with the choices recorded now preselected.

import { html } from "../render.js";
import { displayVersion } from "../version.js";

/** recordedSummary words the recorded choices: each answered group's
 * picks, "Step > Group: A, B". */
function recordedSummary(choices) {
  const lines = [];
  for (const step of choices?.steps ?? []) {
    for (const group of step.groups ?? []) {
      const picks = (group.choices ?? []).map((c) => c.name).join(", ");
      lines.push(`${step.name} > ${group.name}: ${picks || "none"}`);
    }
  }
  return lines;
}

/** FomodReconfigurePlanView renders core.FomodReconfigurePlan. */
export function FomodReconfigurePlanView({ plan }) {
  const lines = recordedSummary(plan.choices);
  return html`
    <div class="plan plan--mod-fomod">
      <p class="plan__summary">
        lmm fetches${" "}
        <span class="mono">${plan.mod.name} ${displayVersion(plan.mod)}</span
        >${" "} again and asks its installer's questions, with your current
        choices selected. Your answers are installed and deployed in place of
        the current files.
      </p>
      ${
        lines.length > 0 &&
        html`<ul class="plan__mods" data-testid="fomod-recorded">
          ${lines.map((line) => html`<li key=${line}>${line}</li>`)}
        </ul>`
      }
    </div>
  `;
}
