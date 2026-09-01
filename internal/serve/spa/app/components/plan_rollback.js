// plan_rollback.js - the confirm modal's renderer for a core.RollbackPlan
// (issue 330's new "rollback" plan kind, kind_rollback.go).

import { html } from "../render.js";

/** RollbackPlanView renders core.RollbackPlan (internal/core/rollback.go). */
export function RollbackPlanView({ plan }) {
  return html`
    <div class="plan plan--rollback">
      <p class="plan__summary">
        Rolling back <span class="mono">${plan.mod.name}</span>
        <span class="mono">${plan.from_version} → ${plan.to_version}</span>.
      </p>

      ${
        plan.locked &&
        html`<p class="plan__note plan__note--warn">
          Locked at <span class="mono">${plan.locked_version}</span> -
          ${plan.refusal || "unlock it first to roll it back."}
        </p>`
      }
      ${
        plan.cache_missing &&
        html`<p class="plan__note plan__note--warn">
          The cached copy of <span class="mono">${plan.to_version}</span> is
          missing - this rollback would fail.
        </p>`
      }
    </div>
  `;
}
