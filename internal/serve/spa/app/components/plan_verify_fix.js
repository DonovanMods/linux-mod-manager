// plan_verify_fix.js - the confirm modal's renderer for a "verify_fix" plan
// (kind_verify_fix.go's core.VerifyReport, issue 332): the Health card's
// per-finding *Repair* and *Repair all*.
//
// The PLAN document (core.VerifyReport) carries no scope of its own - it is
// the frozen dry-run report, byte-identical whether the request behind it
// named a mod_filter or not (kind_verify_fix.go's own doc comment: ModFilter
// is carried on the REQUEST, not echoed on the response). So the scope this
// view announces ("repairing just X" vs "the whole profile") is read off
// `modal.options.mod_filter` - the plan-time request cards.js opened this
// modal with - which is why this renderer, unlike every sibling but
// plan_updates.js, needs `modal` threaded down alongside `plan`
// (confirmplan.js's own addition for the two of them).

import { html } from "../render.js";
import { findingLabel } from "../verify.js";

export function VerifyFixPlanView({ plan, modal }) {
  const findings = (plan?.result?.findings ?? []).filter(
    (f) => f.status !== "ok",
  );
  const scope = modal?.options?.mod_filter;

  return html`
    <div class="plan plan--verify-fix">
      <p class="plan__summary">
        ${
          scope
            ? html`Repairing findings for
                <span class="mono">${scope}</span> only.`
            : "Repairing every fixable finding in this profile."
        }
      </p>
      <p class="plan__note">
        A repair still runs the profile-scoped merged-pak resync and
        deploy-convergence passes regardless of scope - it may resync a stale
        merged pak or sweep a stale deployment even when only one mod's findings
        are otherwise addressed.
      </p>

      ${
        findings.length === 0
          ? html`<p class="plan__note">No findings to report.</p>`
          : html`
              <ul class="plan__paths">
                ${findings.map(
                  (f, i) => html`
                    <li key=${f.mod_id + "/" + (f.file_id || i)}>
                      <span class="plan__mod-name"
                        >${f.mod_name || f.mod_id}</span
                      >${" "}
                      <span class="plan__mod-detail">${findingLabel(f)}</span>
                      ${
                        // htm-ws-ok: "(not fixable)" already opens with its
                        // own leading space when this renders. The engine's
                        // own reason (issue 334) rides along as the title,
                        // so hovering says WHY without lengthening a row
                        // that is already one of many in this list.
                        !f.fixable &&
                        html`<span
                          class="plan__mod-detail plan__note--warn"
                          title=${f.fixable_reason || ""}
                        >
                          (not fixable)</span
                        >`
                      }
                    </li>
                  `,
                )}
              </ul>
            `
      }
    </div>
  `;
}
