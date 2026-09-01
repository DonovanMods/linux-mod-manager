// planrenderers.js - which component renders which plan kind.
//
// THE EXTENSION POINT. The confirm-plan framework (confirmplan.js) is one
// framework for every mutation this UI will ever have: a later unit that
// wires install, uninstall, an update batch, a profile switch or a verify
// repair adds ONE line to the table below and one renderer file beside it.
// It does not add a second modal, a second plan/confirm/job pipeline, or a
// bespoke confirm dialog of its own - the implementation plan's pre-flight
// is explicit that Unit 3 lands this and later units consume it
// (docs/plans/2026-08-31-webui-impl.md §Pre-flight, "U3 confirm framework ↔
// U4-U7 mutations").
//
// A kind with no entry is NOT an error: it falls back to GenericPlanView,
// which renders the plan document as it stands. That is deliberate - a
// mutation wired before its renderer exists still previews honestly rather
// than showing an empty modal with a live Confirm button under it.
//
// The table is a plain object of explicit imports rather than a
// self-registering side-effect import, so a renderer that stops being
// imported is a build-visible missing name here rather than a kind that
// silently degrades to the generic fallback.

import { html } from "../render.js";
import { DocumentView } from "./documentview.js";
import { DeployPlanView } from "./plan_deploy.js";
import { UninstallPlanView } from "./plan_uninstall.js";
import { RollbackPlanView } from "./plan_rollback.js";

// issue 330 carry-2: "the first unit wiring a kind without a renderer adds
// an explicit [E2E] scenario." "updates" is that kind here, deliberately:
// its plan document (updatesBatchPlan, kind_updates.go) is a SELECTION, not
// a per-mod preview worth a bespoke renderer of its own, and the batch UI
// that would actually want one - checkboxes, a running per-item tally - is
// Unit 6's (the update-batch modal). Wiring it through GenericPlanView now,
// honestly, is better than either leaving it unwired or building a
// throwaway renderer this unit would just delete again.
const renderers = {
  deploy: DeployPlanView,
  uninstall: UninstallPlanView,
  rollback: RollbackPlanView,
};

/** GenericPlanView is the fallback: the plan document, rendered as data. */
export function GenericPlanView({ plan }) {
  return html`
    <div class="plan plan--generic">
      <p class="plan__note">
        This mutation has no dedicated preview yet; here is the plan exactly as
        the server computed it.
      </p>
      <${DocumentView} value=${plan} />
    </div>
  `;
}

/** planRendererFor returns kind's renderer, or the generic fallback. */
export function planRendererFor(kind) {
  return renderers[kind] ?? GenericPlanView;
}
