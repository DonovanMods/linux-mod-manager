// plan_profile_apply.js - the confirm modal's renderer for a
// core.ProfileApplyPlan (kind_profile_apply.go, `lmm profile apply`),
// issue 334: make what is installed match what the profile lists.
//
// The one thing this preview must not hide is a to_install entry that
// ALREADY FAILED to resolve. PlanProfileApply resolves every entry against
// its source at plan time and records the failure as data
// (ProfileApplyInstall.Error), keeping the entry in place so the apply
// reports it at that position and carries on (kind_profile_apply.go's own
// header comment). A preview that showed only the resolvable entries would
// let someone commit to a convergence that cannot converge - so a failed
// row is rendered as a warning, in place, with the source's own words.

import { html } from "../render.js";

/** ModList renders one bucket of installed mods (to_enable/to_disable),
 * which carry their own names. */
function ModList({ heading, mods, testID }) {
  if (!mods || mods.length === 0) return null;
  return html`
    <section class="plan__section">
      <p class="plan__heading">${heading} (${mods.length})</p>
      <ul class="plan__paths" data-testid=${testID}>
        ${mods.map(
          (m) => html`
            <li key=${`${m.source_id}:${m.id}`} class="plan__mod">
              <span class="plan__mod-name">${m.name}</span>${" "}
              <span class="plan__mod-detail mono">${m.version}</span>
            </li>
          `,
        )}
      </ul>
    </section>
  `;
}

/** installLabel names one ProfileApplyInstall row. The resolved mod's own
 * name is the best label available; an entry whose source lookup FAILED has
 * no mod at all, so its reference is all there is to name it by. */
function installLabel(entry) {
  if (entry.mod?.name) return entry.mod.name;
  const ref = entry.ref ?? {};
  return `${ref.source_id}:${ref.mod_id}`;
}

/** ProfileApplyPlanView renders core.ProfileApplyPlan
 * (internal/core/profile_apply.go). */
export function ProfileApplyPlanView({ plan }) {
  const toInstall = plan.to_install ?? [];
  const failed = toInstall.filter((entry) => entry.error);

  return html`
    <div class="plan plan--profile-apply">
      <p class="plan__summary">
        Making <span class="mono">${plan.profile}</span> match what it lists.
      </p>

      ${
        plan.no_changes &&
        html`<p class="plan__note">
          Nothing to do - what is installed already matches the profile.
        </p>`
      }
      ${
        failed.length > 0 &&
        html`<p class="plan__note plan__note--warn">
          ${failed.length} of these could not be resolved at their source and
          will be reported as failures.
        </p>`
      }
      ${
        toInstall.length > 0 &&
        html`
          <section class="plan__section">
            <p class="plan__heading">Install (${toInstall.length})</p>
            <ul class="plan__paths" data-testid="profile-apply-to-install">
              ${toInstall.map((entry) => {
                const ref = entry.ref ?? {};
                return html`
                  <li
                    key=${`${ref.source_id}:${ref.mod_id}`}
                    class="plan__mod ${entry.error ? "plan__mod--warn" : ""}"
                  >
                    <span class="plan__mod-name">${installLabel(entry)}</span
                    >${" "}
                    <span class="plan__mod-detail mono"
                      >${entry.version || ref.version}</span
                    >${" "}
                    ${
                      entry.replaces &&
                      html`<span class="plan__mod-detail"
                        >replacing ${entry.replaces.version}</span
                      >`
                    }
                    ${
                      !entry.error &&
                      entry.cached &&
                      html`<span class="plan__mod-detail">already cached</span>`
                    }
                    ${
                      entry.error &&
                      html`<span class="plan__mod-detail plan__mod-detail--warn"
                        >${entry.error}</span
                      >`
                    }
                  </li>
                `;
              })}
            </ul>
          </section>
        `
      }

      <${ModList}
        heading="Enable and deploy"
        mods=${plan.to_enable ?? []}
        testID="profile-apply-to-enable"
      />
      <${ModList}
        heading="Undeploy and disable"
        mods=${plan.to_disable ?? []}
        testID="profile-apply-to-disable"
      />
    </div>
  `;
}
