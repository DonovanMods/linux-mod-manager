// plan_update_from_archive.js - the confirm modal's renderer for a
// core.UpdateFromArchivePlan (issue 530, kind_update_from_archive.go):
// "Update from file…" over an uploaded archive.
//
// What the user most needs to see before confirming is WHICH file the
// archive was recognised as, because that is what later update checks will
// compare against. A mismatch - the archive is not the file the update check
// advertised, say another loader's build - is stated as a warning, and
// confirmplan.js turns Confirm into "Update anyway", which is the answer
// (accept_mismatch) Apply needs; a lock turns Confirm off.
//
// VersionPrompt is the other half: a plan refused because nothing says
// which version the archive is (core.ArchiveVersionRequiredError) asks for
// it, and re-plans with it, instead of leaving the user at a dead end.

import { html, useState } from "../render.js";
import { PlanAdvanced, ApplyOption } from "./planoptions.js";
import { useSourceName } from "../sourcenames.js";

function matchSentence(plan, sourceName) {
  const f = plan.matched_file;
  const name = f ? html`<span class="mono">${f.file_name}</span>` : null;
  switch (plan.match) {
    case "advertised":
      return html`It is the update ${sourceName} advertised:${" "} ${name}.`;
    case "listed":
      return html`It is ${sourceName}'s file${" "}${name}.`;
    case "mismatch":
      return null;
    default:
      return plan.mod.source_id === "local"
        ? null
        : html`${sourceName} lists no file of this name.`;
  }
}

/** UpdateFromArchivePlanView renders core.UpdateFromArchivePlan
 * (internal/core/update_from_archive.go). */
export function UpdateFromArchivePlanView({ plan, modal, actions }) {
  const warnings = plan.warnings ?? [];
  const hooks = plan.hooks ?? [];
  const files = plan.files ?? [];
  const sourceName = useSourceName(plan.mod.source_id, true);
  const sentence = matchSentence(plan, sourceName);

  return html`
    <div class="plan plan--update-from-archive" data-match=${plan.match}>
      <p class="plan__summary">
        Updating <span class="mono">${plan.mod.name}</span>${" "}
        <span class="mono">${plan.from_version} → ${plan.to_version}</span
        >${" "}from <span class="mono">${plan.archive_name}</span>.
      </p>
      ${sentence && html`<p class="plan__note">${sentence}</p>`}
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
          This is not the update ${sourceName} advertised${" "} (<span
            class="mono"
            >${plan.advertised?.file_name}</span
          >). Update anyway only if it is the build you want.
        </p>`
      }
      ${
        plan.locked &&
        html`<p class="plan__note plan__note--warn">
          ${plan.refusal || "This mod is locked; unlock it to update it."}
        </p>`
      }

      <section class="plan__section">
        <h3 class="plan__heading">Files (${files.length})</h3>
        ${
          files.length > 0
            ? html`<ul class="plan__paths">
                ${files.map((f) => html`<li key=${f} class="mono">${f}</li>`)}
              </ul>`
            : html`<p class="plan__note">
                No standalone files - this update contributes to the merged
                artifact only.
              </p>`
        }
      </section>
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
      <p class="plan__note">
        The current version stays in the cache, so the update can be rolled
        back.
      </p>

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
          hint="lmm update --force: carry on past a failing before-update hook."
        />
      <//>
    </div>
  `;
}

/**
 * VersionPrompt asks for the version a plan could not work out
 * (details.version_required) and re-plans the same upload with it.
 */
export function VersionPrompt({ details, actions }) {
  const [version, setVersion] = useState("");
  const submit = (e) => {
    e.preventDefault();
    const v = version.trim();
    if (v) actions.replanWith({ version: v });
  };
  return html`
    <form
      class="version-prompt"
      data-testid="version-prompt"
      onSubmit=${submit}
    >
      <p class="plan__note">
        Nothing says which version${" "}
        <span class="mono">${details.archive_name}</span> is. Enter it to
        continue.
      </p>
      <div class="version-prompt__row">
        <label class="plan__control">
          Version
          <input
            type="text"
            name="version"
            autocomplete="off"
            value=${version}
            onInput=${(e) => setVersion(e.currentTarget.value)}
          />
        </label>
        <button
          type="submit"
          class="button"
          data-action="replan-version"
          disabled=${version.trim() === ""}
        >
          Use this version
        </button>
      </div>
    </form>
  `;
}
