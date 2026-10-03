// confirmplan.js - the confirm-plan modal: the SPA's sibling of the CLI's
// confirmation prompt (docs/plans/2026-08-31-serve-spa-design.md §Modals).
//
// THE ONE MUTATION PIPELINE. Every mutation in this application - this
// unit's deploy, and every kind later units wire - travels the same three
// steps, and they are the backend's own (docs/plans/2026-08-30-serve-design
// .md): POST /api/v1/plans/{kind} computes a plan and hands back a
// single-use handle; this modal renders THAT PLAN DOCUMENT so what is about
// to happen is on screen before anything is asked of the machine; Confirm
// redeems the handle with POST /api/v1/jobs and the initiating control
// morphs into the job's progress.
//
// The reason it is a framework rather than a dialog: a plan cannot survive
// a round trip through the browser (every core plan embeds an unexported
// freshness snapshot its Apply re-checks), so the client only ever holds
// the handle. Which means EVERY mutation has this exact shape whether it
// wants it or not - and one implementation of it is the difference between
// a UI where mutations behave the same everywhere and one where each screen
// invented its own.
//
// What is NOT here: what any plan looks like (planrenderers.js), the modal
// chrome (modal.js), and what a running job looks like (jobprogress.js).

import { html } from "../render.js";
import { Modal } from "./modal.js";
import { ErrorDetails } from "./errordetails.js";
import { planRendererFor } from "./planrenderers.js";
import { VersionPrompt } from "./plan_update_from_archive.js";
import { codeSpans } from "../errortext.js";

/**
 * ConfirmPlanModal renders whatever the store's `modal` slice holds, which
 * is one of four states (see main.js's openPlan/confirmPlan):
 *
 *   planning  the plan is being computed - nothing to confirm yet
 *   ready     the plan document is in hand; Confirm redeems it
 *   starting  Confirm was pressed and POST /api/v1/jobs is in flight
 *   error     the plan (or the job start) failed; there is nothing to apply
 *
 * The error state deliberately offers NO Confirm: an empty modal with a
 * live confirm button under it invites the user to apply nothing, which is
 * the same I3 rule the read surface follows - a failure is never rendered
 * as an all-clear.
 */
export function ConfirmPlanModal({ modal, state, actions }) {
  // type: "plan" self-guards this modal against the shared slot's other two
  // shapes (reorder/profiles, main.js#openReorderModal/openProfilesModal) -
  // store.js's own doc comment: "another shape in this same slot".
  if (modal?.type !== "plan") return null;

  const {
    kind,
    title,
    status,
    plan,
    error,
    details,
    confirmLabel,
    openerSelector,
  } = modal;
  const busy = status === "starting";
  const PlanView = planRendererFor(kind);

  // m5, unit 6 fix wave: an "updates" plan with nothing in it (every
  // selected mod dropped no update to offer, kind_updates.go's own
  // planUpdatesKind puts them in NotFound instead) has nothing for Confirm
  // to apply - library.js/cards.js both filter their own selection before
  // opening this modal, but Confirm staying disabled here too is what makes
  // that a belt-and-braces guarantee rather than a UI convention this modal
  // itself has to trust.
  const emptyUpdatesPlan =
    kind === "updates" &&
    status === "ready" &&
    (plan?.updates?.length ?? 0) === 0;

  // N-1, epic re-review: a re-link plan that has already refused itself
  // (plan.refusal - a locked ref the user has just pointed at a new
  // identity) has nothing for Confirm to apply. plan_mod_relink.js renders
  // the refusal loudly, but the CLI's own answer is to refuse before ever
  // calling Apply (cmd/lmm/mod_edit.go), and this modal should not offer a
  // click the machine has already said no to.
  const relinkRefused =
    kind === "mod_relink" && status === "ready" && Boolean(plan?.refusal);
  // Issue 530: the same for an update from file of a locked mod - Apply
  // refuses a lock outright, so there is nothing for Confirm to start.
  const fromFileRefused =
    kind === "update_from_archive" &&
    status === "ready" &&
    Boolean(plan?.refusal);

  // The type-the-name gate (C-3): a kind listed in typedNameFor keeps
  // Confirm disabled until the user has typed back the thing they are about
  // to destroy. `purge` is the one that earns it - it undeploys an entire
  // profile, and with its own uninstall option set it deletes every mod
  // record behind it, neither of which any other single click in this
  // application can do.
  //
  // Enforced HERE rather than inside the renderer because Confirm lives
  // here: a renderer that owned its own disabled state would be asking this
  // modal to trust it, and the whole point of the gate is that the modal
  // does not have to.
  const requiredName = typedNameFor[kind]?.(plan);
  const nameTyped =
    !requiredName || (modal.confirmationText ?? "").trim() === requiredName;

  // Issue 461 (review F1): a flag-only switch (plan_switch.js's own
  // plan.flag_only branch) only marks a profile active - it deploys and
  // removes nothing - so its Confirm cannot keep the "Switch and deploy"
  // label topbar.js sets before the plan even exists. The plan is the
  // thing that knows which case this is, so it overrides the label rather
  // than the opener guessing up front.
  //
  // Issue 530: an archive that is not the file the update check advertised
  // is a question this click answers - main.js#confirmPlan sends
  // accept_mismatch for exactly this plan - so the label says what the
  // click decides.
  const effectiveConfirmLabel =
    kind === "switch" && plan?.flag_only
      ? "Mark as active"
      : kind === "update_from_archive" && plan?.match === "mismatch"
        ? "Update anyway"
        : confirmLabel;

  const footer =
    status === "error"
      ? html`
          <button
            type="button"
            class="button"
            data-action="cancel"
            onClick=${actions.closePlan}
          >
            Close
          </button>
        `
      : html`
          <button
            type="button"
            class="button"
            data-action="cancel"
            disabled=${busy}
            onClick=${actions.closePlan}
          >
            Cancel
          </button>
          <button
            type="button"
            class="button button--primary"
            data-action="confirm"
            disabled=${status !== "ready" || emptyUpdatesPlan || relinkRefused || fromFileRefused || !nameTyped}
            onClick=${actions.confirmPlan}
          >
            ${busy ? "Starting…" : (effectiveConfirmLabel ?? "Confirm")}
          </button>
        `;

  return html`
    <${Modal}
      kind=${kind}
      title=${title}
      onClose=${busy ? noop : actions.closePlan}
      footer=${footer}
      openerSelector=${openerSelector}
    >
      ${
        status === "planning"
          ? modal.upload
            ? html`<${UploadProgress} upload=${modal.upload} />`
            : html`<p class="modal__pending">Computing the plan…</p>`
          : status === "error"
            ? details?.version_required
              ? html`<${VersionPrompt} details=${details} actions=${actions} />`
              : html`
                  <div class="modal__error">
                    <p>${codeSpans(error)}</p>
                    <${ErrorDetails} details=${details} actions=${actions} />
                  </div>
                `
            : html`<${PlanView}
                plan=${plan}
                modal=${modal}
                state=${state}
                actions=${actions}
              />`
      }
    <//>
  `;
}

/**
 * UploadProgress is the planning state of a plan that first has a file to
 * send (issue 530's "Update from file…"): the archive's name and how much of
 * it has reached the server. Cancel closes the modal, which stops the
 * upload (main.js#updateFromFileChosen).
 */
function UploadProgress({ upload }) {
  const pct = upload.total
    ? Math.round((upload.loaded / upload.total) * 100)
    : 0;
  return html`
    <div class="modal__pending" data-testid="upload-progress">
      <p>Uploading <span class="mono">${upload.name}</span>…</p>
      <div class="job-progress">
        <div class="job-progress__bar">
          <div class="job-progress__fill" style=${`width: ${pct}%`}></div>
        </div>
        <span class="job-progress__text">${`${pct}%`}</span>
      </div>
    </div>
  `;
}

// typedNameFor maps a kind to the exact string its Confirm demands back
// from the user, or undefined for the kinds (every other one) that ask for
// nothing but a click. The renderer draws the input; this table is what
// makes it load-bearing.
const typedNameFor = {
  purge: (plan) => plan?.profile,
};

// noop holds the modal open while a job start is in flight: Escape and the
// scrim must not close a modal whose confirm has already been sent, or the
// user loses sight of a mutation they cannot now cancel.
function noop() {}
