// plan_updates.js - the confirm modal's renderer for the "updates" batch
// kind (core.UpdateBatchPlan, served by kind_updates.go), replacing
// GenericPlanView for this one kind (issue 330 carry-3, issue 332):
// checkboxes to drop a row, a per-item tally, and NotFound rows named
// rather than silently absorbed into the count.
//
// A batch plan IS a selection (core.UpdateBatchPlan's own doc comment: the
// items are re-planned one at a time inside the apply, so the batch plan
// carries the chosen updates rather than N pre-computed single-mod plans),
// so "dropping a row" can only mean re-planning with a smaller selection:
// PlanUpdateBatch computes plan.Updates once, at Plan time, and
// ApplyUpdateBatch just walks it - there is no apply-time filter option to
// smuggle a drop through (issue 332's review confirmed there is none to
// add). So unchecking a row calls actions.openPlan again, with the
// SAME kind/origin/title/confirmLabel modal.js already opened this with
// (threaded down as `modal`, confirmplan.js's own addition for this file)
// and a `mods` list one entry shorter - a fresh plan_id, same modal slot,
// same "confirm redeems a handle" contract every other kind already has.
// The unstarted `plan_id` this replaces simply expires on the plan store's
// own TTL, same as a Cancelled plan always has.

import { html } from "../render.js";
import { lockedNote, modKey } from "../modrows.js";
import { PlanAdvanced, ApplyOption } from "./planoptions.js";

/** rowKey identifies one UpdateBatchPlan row - the same "source:id" key
 * the plan's own request/mods took, and NotFound already reports in. */
function rowKey(update) {
  return modKey(update.installed_mod);
}

export function UpdatesBatchPlanView({ plan, modal, actions }) {
  const updates = plan.updates ?? [];
  const notFound = plan.not_found ?? [];
  // Owner item 3, unit 8 gate review: the confirm step's whole job is to say
  // what will happen, and a locked row is one ApplyUpdateBatch will REFUSE
  // (#97) rather than update. Rendering it as "1.0 → 2.0" like every other
  // row promised an update that could not happen; the count is stated up
  // front and the row itself says so instead of showing an arrow.
  const lockedCount = updates.filter((u) => u.locked).length;

  /** drop re-plans with every CURRENTLY planned row except `key`. A no-op
   * when it is the only row left - nothing meaningful for Confirm to apply
   * to, so the checkbox simply can't be unchecked that far. */
  function drop(key) {
    const remaining = updates.map(rowKey).filter((k) => k !== key);
    if (remaining.length === 0) return;
    actions.openPlan({
      kind: "updates",
      origin: modal.origin,
      title: modal.title,
      confirmLabel: modal.confirmLabel,
      options: { mods: remaining },
      // Forwarded, not lost: a drop's own re-plan replaces this modal in
      // place (same slot, fresh plan_id), and the caller's own "clear my
      // selection once this is actually confirmed" (m4/I2) must survive
      // that just as it survives every other re-render of this modal.
      onConfirmed: modal.onConfirmed,
      // Forwarded for the same reason (issue 334): a re-plan that dropped
      // the modal's focus-return target would strand focus on the body.
      openerSelector: modal.openerSelector,
    });
  }

  return html`
    <div class="plan plan--updates">
      <p class="plan__summary">
        ${`${updates.length} update${updates.length === 1 ? "" : "s"} selected.`}
      </p>
      ${
        lockedCount > 0 &&
        html`<h3 class="plan__heading plan__heading--warn">
          ${`${lockedCount} of them will be skipped — locked.`}
        </h3>`
      }
      ${
        updates.length > 0 &&
        html`
          <ul class="plan__paths" data-testid="updates-batch-rows">
            ${updates.map((u) => {
              const key = rowKey(u);
              return html`
                <li key=${key} class="plan__mod">
                  <label>
                    <input
                      type="checkbox"
                      checked
                      disabled=${updates.length === 1}
                      aria-label=${`Drop ${u.installed_mod.name} from this batch`}
                      onChange=${() => drop(key)}
                    />
                    <span class="plan__mod-name">${u.installed_mod.name}</span
                    >${" "}
                    ${
                      u.locked
                        ? html`<span class="plan__mod-detail"
                            >${`will be skipped — ${lockedNote(u)}`}</span
                          >`
                        : html`<span class="plan__mod-detail mono"
                            >${u.installed_mod.version} → ${u.new_version}</span
                          >`
                    }
                  </label>
                </li>
              `;
            })}
          </ul>
        `
      }
      ${
        notFound.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading plan__heading--warn">
              No longer offers an update (${notFound.length})
            </h3>
            <ul class="plan__paths">
              ${notFound.map(
                (key) => html`<li key=${key} class="mono">${key}</li>`,
              )}
            </ul>
          </section>
        `
      }

      <${PlanAdvanced}>
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="skip_hooks"
          label="Skip hooks"
          hint="lmm --no-hooks."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="force"
          label="Force"
          hint="lmm --force. Carry on past a failure that would otherwise stop the flow."
        />
      <//>
    </div>
  `;
}
