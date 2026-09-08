// plan_updates.js - the confirm modal's renderer for the "updates" batch
// kind (kind_updates.go's updatesBatchPlan), replacing GenericPlanView for
// this one kind (issue 330 carry-3, issue 332): checkboxes to drop a row, a
// per-item tally, and NotFound rows named rather than silently absorbed
// into the count.
//
// There is no core.UpdatePlan batch type to render (kind_updates.go's own
// doc comment: the batch is a SELECTION, applied as N single-mod plans) -
// updatesBatchPlan already IS that selection, so "dropping a row" can only
// mean re-planning with a smaller selection: kind_updates.go's
// planUpdatesKind computes p.Updates once, at Plan time, and Apply just
// walks it - there is no apply-time filter option to smuggle a drop through
// (this unit's gate keeps the wire frozen; issue 332 review confirmed there is
// none to add). So unchecking a row calls actions.openPlan again, with the
// SAME kind/origin/title/confirmLabel modal.js already opened this with
// (threaded down as `modal`, confirmplan.js's own addition for this file)
// and a `mods` list one entry shorter - a fresh plan_id, same modal slot,
// same "confirm redeems a handle" contract every other kind already has.
// The unstarted `plan_id` this replaces simply expires on the plan store's
// own TTL, same as a Cancelled plan always has.

import { html } from "../render.js";
import { modKey } from "../modrows.js";

/** rowKey identifies one updatesBatchPlan row - the same "source:id" key
 * the plan's own request/mods took, and NotFound already reports in. */
function rowKey(update) {
  return modKey(update.installed_mod);
}

export function UpdatesBatchPlanView({ plan, modal, actions }) {
  const updates = plan.updates ?? [];
  const notFound = plan.not_found ?? [];

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
    });
  }

  return html`
    <div class="plan plan--updates">
      <p class="plan__summary">
        ${updates.length} update${updates.length === 1 ? "" : "s"} selected.
      </p>

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
                    <span class="plan__mod-name">${u.installed_mod.name}</span>
                    <span class="plan__mod-detail mono"
                      >${u.installed_mod.version} → ${u.new_version}</span
                    >
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
            <p class="plan__heading plan__heading--warn">
              No longer offers an update (${notFound.length})
            </p>
            <ul class="plan__paths">
              ${notFound.map(
                (key) => html`<li key=${key} class="mono">${key}</li>`,
              )}
            </ul>
          </section>
        `
      }
    </div>
  `;
}
