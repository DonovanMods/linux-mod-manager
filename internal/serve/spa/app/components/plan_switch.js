// plan_switch.js - the confirm modal's renderer for a core.SwitchPlan
// (kind_switch.go, `lmm profile switch`), issue 334.
//
// A switch is the one mutation in this application that changes the whole
// context underneath the user - which profile the game deploys - so the
// preview leads with the move itself ("default -> hardcore") and only then
// lists the three buckets PlanProfileSwitch computed. The buckets are the
// entire reason the confirm exists: "switch to hardcore" says nothing about
// the eight mods it is about to undeploy.
//
// PriorVersions is rendered against the to_install row it belongs to rather
// than as a fourth list. It exists because a to_install entry is a
// domain.ModReference with no room for the version being REPLACED (#96
// convergence), so the map is the only place that fact lives - and "install
// 1.4" beside "replacing 1.2" is a materially different sentence from
// "install 1.4" alone.

import { html } from "../render.js";

/** refLabel names a domain.ModReference the same way plan_profile_import.js
 * does - it carries no display name of its own, so "source:mod @ version"
 * is the honest rendering. */
function refLabel(ref) {
  const at = ref.version ? ` @ ${ref.version}` : "";
  return `${ref.source_id}:${ref.mod_id}${at}`;
}

/** ModList renders one bucket of installed mods (to_enable/to_disable),
 * which - unlike a bare reference - do carry their own names. */
function ModList({ heading, mods, testID }) {
  if (!mods || mods.length === 0) return null;
  return html`
    <section class="plan__section">
      <h3 class="plan__heading">${heading} (${mods.length})</h3>
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

/** SwitchPlanView renders core.SwitchPlan (internal/core/switch.go). */
export function SwitchPlanView({ plan }) {
  const toDisable = plan.to_disable ?? [];
  const toEnable = plan.to_enable ?? [];
  const toInstall = plan.to_install ?? [];
  const priorVersions = plan.prior_versions ?? {};

  return html`
    <div class="plan plan--switch">
      <p class="plan__summary">
        Switching from <span class="mono">${plan.from}</span> to${" "}
        <span class="mono">${plan.to}</span>.
      </p>

      ${
        plan.already_active &&
        html`<p class="plan__note">
          <span class="mono">${plan.to}</span> is already the active profile -
          there is nothing to move.
        </p>`
      }
      ${
        !plan.already_active &&
        plan.no_changes &&
        html`<p class="plan__note">
          Both profiles hold the same mods, so only the active profile itself
          changes.
        </p>`
      }

      <${ModList}
        heading="Undeploy and disable"
        mods=${toDisable}
        testID="switch-to-disable"
      />
      <${ModList}
        heading="Enable and deploy"
        mods=${toEnable}
        testID="switch-to-enable"
      />
      ${
        toInstall.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">
              Download and install (${toInstall.length})
            </h3>
            <ul class="plan__paths" data-testid="switch-to-install">
              ${toInstall.map((ref) => {
                const prior = priorVersions[`${ref.source_id}:${ref.mod_id}`];
                return html`
                  <li key=${`${ref.source_id}:${ref.mod_id}`} class="plan__mod">
                    <span class="mono">${refLabel(ref)}</span>${" "}
                    ${
                      prior &&
                      html`<span class="plan__mod-detail"
                        >replacing ${prior.version}</span
                      >`
                    }
                  </li>
                `;
              })}
            </ul>
          </section>
        `
      }
    </div>
  `;
}
