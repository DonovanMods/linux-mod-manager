// plan_profile_import.js - the confirm modal's renderer for a
// core.ImportPlan (kind_profile_import.go, issue 332): what will be created,
// whether the name collides, and the three domain.ModReference buckets -
// already installed, needing a redownload, or missing entirely.

import { html } from "../render.js";

/** refLabel names a domain.ModReference - it carries no display name of its
 * own (domain.ModReference's own fields: source_id, mod_id, version,
 * file_ids, locked), so "source:mod" @ version is the honest rendering. */
function refLabel(ref) {
  const at = ref.version ? ` @ ${ref.version}` : "";
  return `${ref.source_id}:${ref.mod_id}${at}`;
}

function RefList({ heading, refs }) {
  if (!refs || refs.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">${heading} (${refs.length})</h3>
      <ul class="plan__paths">
        ${refs.map(
          (ref) =>
            html`<li key=${`${ref.source_id}:${ref.mod_id}`} class="mono">
              ${refLabel(ref)}
            </li>`,
        )}
      </ul>
    </section>
  `;
}

export function ProfileImportPlanView({ plan, actions }) {
  const installed = plan.installed ?? [];
  const needsRedownload = plan.needs_redownload ?? [];
  const missing = plan.missing ?? [];
  const pending = needsRedownload.length + missing.length;

  /** setInstall is the one apply-time choice this preview offers -
   * core.ProfileImportOptions.Install, the v2 Phase 3 Ruling 1 case in its
   * purest form: fully derivable from the plan (whether `pending` is
   * non-zero) before Apply ever runs. Force is the OTHER apply-time
   * option, offered only when the plan says the name collides. */
  function setInstall(e) {
    actions.setPlanOptions({ install: e.currentTarget.checked });
  }
  function setForce(e) {
    actions.setPlanOptions({ force: e.currentTarget.checked });
  }

  return html`
    <div class="plan plan--profile-import">
      <p class="plan__summary">
        Importing <span class="mono">${plan.profile?.name}</span> as a new
        profile${installed.length > 0 ? ` (${installed.length} mod${installed.length === 1 ? "" : "s"} already installed)` : ""}.
      </p>

      ${
        plan.exists &&
        html`
          <p class="plan__note plan__note--warn">
            A profile named <span class="mono">${plan.profile?.name}</span>
            already exists.
          </p>
          <label class="plan__control">
            <input type="checkbox" onChange=${setForce} />
            Overwrite the existing profile
          </label>
        `
      }
      ${
        pending > 0 &&
        html`
          <label class="plan__control">
            <input type="checkbox" onChange=${setInstall} />
            Download and install ${pending} pending
            mod${pending === 1 ? "" : "s"}
          </label>
          <p class="plan__note">
            Left unchecked, the profile is still saved and every pending mod is
            recorded as skipped.
          </p>
        `
      }

      <${RefList} heading="Already installed" refs=${installed} />
      <${RefList} heading="Needs redownload" refs=${needsRedownload} />
      <${RefList} heading="Missing entirely" refs=${missing} />
    </div>
  `;
}
