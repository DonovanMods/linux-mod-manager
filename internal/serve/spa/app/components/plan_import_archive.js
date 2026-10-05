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
//
// The same plan is issue 535's "Install from file…": an import with the
// failed install's identity, whose plan says (plan.match) whether the
// archive is the file the install tried. That case reads as an install -
// which file the archive was recognised as, a mismatch as a warning beside
// Confirm's "Install anyway" (confirmplan.js), and the dependencies it does
// not install, by name.

import { html } from "../render.js";
import { PlanAdvanced, ApplyOption } from "./planoptions.js";
import { useSourceName } from "../sourcenames.js";

/** installMatchNote is what the archive was recognised as, for an install
 * from a file - nothing for a mismatch (its warning says it) or a name the
 * source does not list (a plan warning says what that means). */
function installMatchNote(plan, sourceName) {
  const f = plan.matched_file;
  if (!f) return null;
  const name = html`<span class="mono">${f.file_name}</span>`;
  switch (plan.match) {
    case "advertised":
      return html`It is the file lmm tried to download:${" "}${name}.`;
    case "listed":
      return html`It is ${sourceName}'s file${" "}${name}.`;
    default:
      return null;
  }
}

/** InstallFromFileFacts is the install-from-file half of the plan: the
 * match, the normalised-name note, the mismatch warning and the unmet
 * dependencies. */
function InstallFromFileFacts({ plan }) {
  const sourceName = useSourceName(plan.mod.source_id, true);
  const note = installMatchNote(plan, sourceName);
  const unmet = plan.unmet_dependencies ?? [];
  return html`
    ${note && html`<p class="plan__note">${note}</p>`}
    ${
      plan.match_normalized &&
      plan.matched_file &&
      html`<p class="plan__note" data-testid="match-normalized">
        Matched <span class="mono">${plan.matched_file.file_name}</span>,
        ignoring the copy number your browser added to the name.
      </p>`
    }
    ${
      plan.match === "mismatch" &&
      html`<p
        class="plan__note plan__note--warn"
        data-testid="archive-mismatch"
      >
        This is not the file lmm tried to download${" "}(<span class="mono"
          >${plan.expected?.file_name}</span
        >). Install anyway only if it is the build you want.
      </p>`
    }
    ${
      unmet.length > 0 &&
      html`
        <section class="plan__section" data-testid="unmet-dependencies">
          <h3 class="plan__heading plan__heading--warn">
            Not installed with it (${unmet.length})
          </h3>
          <p class="plan__note">
            Installing from a file installs this mod alone. It needs these too -
            install them from Search:
          </p>
          <ul class="plan__paths">
            ${unmet.map(
              (d) =>
                html`<li key=${`${d.source_id}:${d.mod_id}`}>
                  ${d.name || html`<span class="mono">${d.source_id}:${d.mod_id}</span>`}
                </li>`,
            )}
          </ul>
        </section>
      `
    }
  `;
}

/** ImportArchivePlanView renders core.ImportArchivePlan (internal/core/import_archive.go). */
export function ImportArchivePlanView({ plan, modal, actions }) {
  const conflicts = plan.conflicts ?? [];
  const warnings = plan.warnings ?? [];
  const hooks = plan.hooks ?? [];
  const files = plan.files ?? [];
  const archiveName = plan.archive.split(/[/\\]/).pop();
  // Issue 535: only an install from a file carries a match.
  const installing = Boolean(plan.match);
  // The version the import records, read once for either summary.
  const version = plan.mod.version;

  return html`
    <div
      class="plan plan--import-archive"
      data-match=${plan.match || undefined}
    >
      ${
        installing
          ? html`<p class="plan__summary">
                Installing <span class="mono">${plan.mod.name}</span>${" "}
                <span class="mono">${version}</span>${" "}from${" "}
                <span class="mono">${archiveName}</span>.
              </p>
              <${InstallFromFileFacts} plan=${plan} />`
          : html`<p class="plan__summary">
              Importing <span class="mono">${archiveName}</span>${" "}as${" "}
              <span class="mono">${plan.mod.name}</span>${" "}
              <span class="mono">${version}</span>${" "}
              ${
                plan.auto_detected
                  ? "(identity parsed from the file name)."
                  : plan.linked_source === "local"
                    ? "(unlinked)."
                    : `(linked to ${plan.linked_source}).`
              }
            </p>`
      }
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
          hint="lmm import --force: import without conflict prompts. Bypasses the conflict refusal outright and overwrites without the Overwrite round-trip - not just carrying on past an ordinary failure."
        />
      <//>
    </div>
  `;
}
