// uninstallbatchmodal.js - the library batch bar's "Uninstall selected"
// (docs/plans/2026-08-31-serve-spa-design.md §Modals: "modals stack at most
// one deep", issue 332): ONE confirm modal listing every selected mod's own
// uninstall plan, never N separate ones.
//
// This is deliberately NOT the confirm-plan framework (confirmplan.js) run
// N times - there is exactly one modal open at a time in this application,
// so "confirm each mod's uninstall" has to be one screen. It plans every
// selected mod as its own "uninstall" plan (N independent POST
// /api/v1/plans/uninstall calls - task-A's report is explicit that a
// server-side batch-uninstall endpoint is deliberately NOT offered, so
// there is nothing else to call), renders each with the SAME
// UninstallPlanView the single-mod confirm modal uses, and on confirm hands
// the N ready plan ids to main.js's startUninstallBatch, which redeems them
// SEQUENTIALLY (the wire's own caveat: sequence per-mod batch jobs).

import { html, useEffect, useState } from "../render.js";
import { Modal } from "./modal.js";
import { plan as planMutation } from "../api.js";
import { UninstallPlanView } from "./plan_uninstall.js";

/** entryKey identifies one mod within this modal - the same "source/id"
 * shape used elsewhere for a per-mod key. */
function entryKey(mod) {
  return `${mod.source_id}/${mod.id}`;
}

export function UninstallBatchModal({ modal, state, actions }) {
  if (modal?.type !== "uninstall-batch") return null;

  const mods = modal.mods;
  // entries is null while every mod's own plan is still being computed;
  // once settled it is one {mod, planID, plan, error} row per mod - a
  // failed plan (the mod vanished, a stale read) is kept and shown rather
  // than silently dropped, matching the confirm-plan framework's own error
  // state never rendering as an all-clear.
  const [entries, setEntries] = useState(null);
  const [confirming, setConfirming] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const context = { game: state.route.game, profile: state.route.profile };
    Promise.all(
      mods.map((mod) =>
        planMutation(
          "uninstall",
          { mod_id: mod.id, source_id: mod.source_id },
          context,
        ).then(
          (res) => ({ mod, planID: res.plan_id, plan: res.plan, error: null }),
          (err) => ({
            mod,
            planID: null,
            plan: null,
            error: err?.message ?? String(err),
          }),
        ),
      ),
    ).then((results) => {
      if (!cancelled) setEntries(results);
    });
    return () => {
      cancelled = true;
    };
    // Plans exactly once, for the mod set this modal was opened with.
  }, []);

  function close() {
    if (confirming) return;
    actions.closeModal();
  }

  async function confirm() {
    // Only mods whose PREVIEW plan actually succeeded - one that already
    // failed here (the mod vanished, a stale read) will fail identically
    // when startUninstallBatch re-plans it, so there is nothing to gain by
    // sending it through. The mod itself is what's sent, not this preview's
    // own plan_id/plan: startUninstallBatch RE-PLANS each one immediately
    // before its own apply (main.js's own doc comment on why - a plan
    // computed for mod 2 before mod 1's uninstall ran is stale the instant
    // mod 1's own apply lands).
    const ready = (entries ?? []).filter((e) => e.planID).map((e) => e.mod);
    if (ready.length === 0) return;
    setConfirming(true);
    // The modal itself closes immediately on confirm, same as the
    // confirm-plan framework's own confirmPlan - the N jobs that follow
    // show their own inline/toast outcome, not a modal sitting open over a
    // multi-job sequence with nothing left for it to say.
    actions.closeModal();
    // modal.onConfirmed (m4/I2, unit 6 fix wave): the caller's own "clear
    // the selection" - deferred to here, not to open, so a Cancel leaves
    // the multi-select (and the batch bar it lives on) exactly as the user
    // left it.
    modal.onConfirmed?.();
    await actions.startUninstallBatch(ready);
  }

  const loading = entries === null;
  const readyCount = loading ? 0 : entries.filter((e) => e.planID).length;
  const failedCount = loading ? 0 : entries.length - readyCount;

  return html`
    <${Modal}
      kind="uninstall-batch"
      title=${`Uninstall ${mods.length} mod${mods.length === 1 ? "" : "s"}`}
      onClose=${confirming ? () => {} : close}
      footer=${html`
        <button
          type="button"
          class="button"
          disabled=${confirming}
          onClick=${close}
        >
          Cancel
        </button>
        <button
          type="button"
          class="button button--danger"
          data-action="confirm"
          disabled=${loading || confirming || readyCount === 0}
          onClick=${confirm}
        >
          ${confirming ? "Starting…" : `Uninstall ${readyCount}`}
        </button>
      `}
    >
      ${
        loading
          ? html`<p class="modal__pending">Computing plans…</p>`
          : html`
              ${
                failedCount > 0 &&
                html`<p class="modal__error">
                  ${failedCount} mod${failedCount === 1 ? "" : "s"} could not be
                  planned and will be skipped.
                </p>`
              }
              ${entries.map(
                (e) => html`
                  <section
                    class="plan__section uninstall-batch__entry"
                    key=${entryKey(e.mod)}
                  >
                    ${
                      e.error
                        ? html`<p class="modal__error">
                            ${e.mod.name}: ${e.error}
                          </p>`
                        : html`<${UninstallPlanView} plan=${e.plan} />`
                    }
                  </section>
                `,
              )}
            `
      }
    <//>
  `;
}
