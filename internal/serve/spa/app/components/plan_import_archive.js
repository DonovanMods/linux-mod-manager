// plan_import_archive.js - the confirm modal's renderer for a
// core.ImportArchivePlan (issue 333, kind_import_archive.go): the archive
// import flow over an uploaded file, driven from the Setup page's Archive
// import section.
//
// The source-link fields (source_id/mod_id) are collected on the Setup
// page's OWN form, before the plan is even requested (setupimport.js) - the
// metadata fetch that resolves them runs at PLAN time (kind_import_archive.
// go's own doc comment), so there is nothing left to edit once the plan is
// on screen. This renderer follows plan_uninstall.js's reference shape:
// read the frozen plan, render the facts, add nothing.
//
// A conflict list on the plan (plan.conflicts) is rendered as a warning with
// no action of its own: the Overwrite affordance for it lives where every
// other job failure's does, in the tray/inline job chip (failures.js's
// nextStepFor, extended to this kind) - Confirm here starts the job as
// asked, and a refusal is answered after the fact, exactly the way
// install's own conflict round trip works.

import { html } from "../render.js";
import { PlanAdvanced, ApplyOption } from "./planoptions.js";

/** ImportArchivePlanView renders core.ImportArchivePlan (internal/core/import_archive.go). */
export function ImportArchivePlanView({ plan, modal, actions }) {
  const conflicts = plan.conflicts ?? [];
  const warnings = plan.warnings ?? [];
  const hooks = plan.hooks ?? [];
  const files = plan.files ?? [];
  const archiveName = plan.archive.split(/[/\\]/).pop();

  return html`
    <div class="plan plan--import-archive">
      <p class="plan__summary">
        Importing <span class="mono">${archiveName}</span>${" "}as${" "}
        <span class="mono">${plan.mod.name}</span>${" "}
        <span class="mono">${plan.mod.version}</span>${" "}
        ${
          plan.auto_detected
            ? "(identity parsed from the file name)."
            : plan.linked_source === "local"
              ? "(unlinked)."
              : `(linked to ${plan.linked_source}).`
        }
      </p>
      ${
        plan.linked_source === "local" &&
        html`<p class="plan__note">
          Local mods won't receive update notifications.
        </p>`
      }

      <section class="plan__section">
        <h3 class="plan__heading">Files (${files.length})</h3>
        ${
          files.length > 0
            ? html`
                <ul class="plan__paths">
                  ${files.map((f) => html`<li key=${f} class="mono">${f}</li>`)}
                </ul>
              `
            : html`<p class="plan__note">
                No standalone files - this import contributes to the merged
                artifact only.
              </p>`
        }
      </section>

      ${
        plan.merged_artifact &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">Merged artifact</h3>
            <p>
              <span class="mono">${plan.merged_artifact.artifact}</span> would
              be affected.
            </p>
          </section>
        `
      }
      ${
        conflicts.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading plan__heading--warn">
              Conflicts (${conflicts.length})
            </h3>
            <ul class="plan__paths">
              ${conflicts.map(
                (c) =>
                  html`<li key=${c.relative_path} class="mono">
                    ${c.relative_path}
                  </li>`,
              )}
            </ul>
            <p class="plan__note plan__note--warn">
              Confirming will fail with these conflicts unless "Overwrite" is
              chosen afterward from the failed job.
            </p>
          </section>
        `
      }
      ${
        warnings.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading plan__heading--warn">Warnings</h3>
            <ul class="plan__paths">
              ${warnings.map((w, i) => html`<li key=${i}>${w}</li>`)}
            </ul>
          </section>
        `
      }
      ${
        hooks.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">Hooks (${hooks.length})</h3>
            <p class="mono plan__hooks">${hooks.join(" → ")}</p>
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
          hint="lmm import --force. Carry on past a failure that would otherwise stop the flow."
        />
      <//>
    </div>
  `;
}
