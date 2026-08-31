// plan_deploy.js - the confirm modal's renderer for a core.DeployPlan.
//
// It is the reference implementation for every later kind's renderer
// (planrenderers.js registers them): read the frozen core plan document,
// render the facts THAT plan carries, and add nothing. A deploy plan
// already answers "which mods, which files, what gets purged first, which
// hooks run, what the merge would produce" - the renderer's whole job is to
// put those on screen in the order a user decides by.

import { html } from "../render.js";

/** DeployPlanView renders core.DeployPlan (internal/core/deploy.go). */
export function DeployPlanView({ plan }) {
  const mods = plan.mods ?? [];
  const purge = plan.purge ?? [];
  const hooks = plan.hooks ?? [];

  if (plan.no_changes) {
    return html`
      <div class="plan plan--deploy">
        <p class="plan__note">
          Nothing to deploy: no mod is selected and there is nothing to purge.
        </p>
      </div>
    `;
  }

  return html`
    <div class="plan plan--deploy">
      <p class="plan__summary">
        Deploying into profile <span class="mono">${plan.profile}</span>.
      </p>

      ${
        purge.length > 0 &&
        html`
          <section class="plan__section">
            <p class="plan__heading">Purge first (${purge.length})</p>
            <ul class="plan__paths">
              ${purge.map((p) => html`<li key=${p} class="mono">${p}</li>`)}
            </ul>
          </section>
        `
      }

      <section class="plan__section">
        <p class="plan__heading">Mods (${mods.length})</p>
        <ul class="plan__mods">
          ${mods.map(
            (mod) => html`
              <li
                key=${`${mod.ref.source_id}/${mod.ref.mod_id}`}
                class="plan__mod ${mod.skipped ? "plan__mod--skipped" : ""}"
              >
                <span class="plan__mod-name">${mod.name}</span>
                ${
                  // Conditional, because a deploy plan's refs carry no
                  // version: planDeploy builds each DeployPlanMod.Ref from
                  // the source and mod id alone (internal/core/deploy.go),
                  // so this is empty for every deploy row today. Rendering
                  // the span anyway would leave a gap that reads as a
                  // missing value rather than an absent field. Filed as a
                  // carry-in - the version a deploy would put on disk is a
                  // fact the confirm modal should be able to show.
                  mod.ref.version &&
                  html`<span class="mono plan__mod-version"
                    >${mod.ref.version}</span
                  >`
                }
                <span class="plan__mod-detail">${modDetail(mod)}</span>
                ${
                  (mod.link ?? []).length > 0 &&
                  html`
                    <ul class="plan__paths">
                      ${mod.link.map((p) => html`<li key=${p} class="mono">${p}</li>`)}
                    </ul>
                  `
                }
                ${
                  (mod.remove ?? []).length > 0 &&
                  html`
                    <p class="plan__removes">
                      Removes ${mod.remove.length} stale
                      path${mod.remove.length === 1 ? "" : "s"}
                    </p>
                  `
                }
              </li>
            `,
          )}
        </ul>
      </section>

      ${
        plan.merged &&
        html`
          <section class="plan__section">
            <p class="plan__heading">Merged artifact</p>
            <p>
              <span class="mono">${plan.merged.artifact}</span> carrying
              ${mergedSummary(plan.merged)}
            </p>
          </section>
        `
      }
      ${
        hooks.length > 0 &&
        html`
          <section class="plan__section">
            <p class="plan__heading">Hooks (${hooks.length})</p>
            <p class="mono plan__hooks">${hooks.join(" → ")}</p>
          </section>
        `
      }
    </div>
  `;
}

/** mergedSummary renders core.MergePlan's two lists as one sentence: the
 * mods whose content the artifact carries (Sources) and the ones deploying
 * raw because conversion is unavailable to them (RawFallbacks). */
function mergedSummary(merged) {
  const sources = (merged.sources ?? []).length;
  const raw = (merged.raw_fallbacks ?? []).length;
  const carried = `${sources} mod${sources === 1 ? "" : "s"}`;
  return raw > 0 ? `${carried}, ${raw} deployed raw.` : `${carried}.`;
}

/** modDetail is one plan row's own status line: why it will not deploy, or
 * how it will. The three states are mutually exclusive in the document
 * (DeployPlanMod: Skipped, Redownload, or a plain link list). */
function modDetail(mod) {
  if (mod.skipped) return `skipped — ${mod.skipped}`;
  if (mod.redownload) return "cache missing — will re-download first";
  const count = (mod.link ?? []).length;
  if (count === 0) {
    return mod.class === "merged"
      ? "rides the merged artifact"
      : "no files to link";
  }
  return `${count} file${count === 1 ? "" : "s"}`;
}
