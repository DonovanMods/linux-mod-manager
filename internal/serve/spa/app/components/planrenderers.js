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
import { InstallPlanView } from "./plan_install.js";
import { UpdatesBatchPlanView } from "./plan_updates.js";
import { VerifyFixPlanView } from "./plan_verify_fix.js";
import { ProfileImportPlanView } from "./plan_profile_import.js";
import { ImportArchivePlanView } from "./plan_import_archive.js";
import { AdoptPlanView } from "./plan_adopt.js";

// issue 332 (issue 330 carry-2's own promise kept): "updates", "verify_fix" and
// "profile_import" each get their real renderer here, replacing the
// GenericPlanView fallback they ran on since the units that first wired
// their own kind (updates: issue 330; verify_fix, profile_import: this
// unit's own kind_verify_fix.go/kind_profile_import.go).
const renderers = {
  deploy: DeployPlanView,
  uninstall: UninstallPlanView,
  rollback: RollbackPlanView,
  install: InstallPlanView,
  updates: UpdatesBatchPlanView,
  verify_fix: VerifyFixPlanView,
  profile_import: ProfileImportPlanView,
  // issue 333's two Setup-surface kinds - GenericPlanView's own fallback
  // list (docs/plans/unit7-carry.md m8) narrows to "switch"/"profile_apply"
  // now that both of these have a real renderer.
  import_archive: ImportArchivePlanView,
  adopt: AdoptPlanView,
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
