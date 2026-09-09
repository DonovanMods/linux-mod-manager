// plan_uninstall.js - the confirm modal's renderer for a core.UninstallPlan
// (issue 330's registration of the second kind, following plan_deploy.js's
// own "read the frozen plan document, render the facts it carries, add
// nothing" pattern - planrenderers.js's own doc comment names it the
// reference implementation for every later kind's renderer).

import { html } from "../render.js";
import { PlanAdvanced, PlanOption, ApplyOption } from "./planoptions.js";

/** UninstallPlanView renders core.UninstallPlan (internal/core/uninstall.go). */
export function UninstallPlanView({ plan, modal, actions }) {
  const files = plan.files ?? [];
  const hooks = plan.hooks ?? [];

  return html`
    <div class="plan plan--uninstall">
      <p class="plan__summary">
        Uninstalling <span class="mono">${plan.mod.name}</span>${" "}
        <span class="mono">${plan.mod.version}</span> from profile${" "}
        <span class="mono">${plan.mod.profile_name}</span>.
      </p>

      ${
        files.length > 0
          ? html`
              <section class="plan__section">
                <h3 class="plan__heading">Files removed (${files.length})</h3>
                <ul class="plan__paths">
                  ${files.map((f) => html`<li key=${f} class="mono">${f}</li>`)}
                </ul>
              </section>
            `
          : html`<p class="plan__note">
              Not currently deployed - nothing to remove from the game
              directory.
            </p>`
      }

      <p class="plan__note" data-testid="uninstall-cache-note">
        ${
          plan.keep_cache
            ? "The cached download is kept."
            : "The cached download is deleted too - reinstalling later re-downloads it."
        }
      </p>

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
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="keep_cache"
          alsoApply
          label="Keep the cached download"
          hint="lmm uninstall --keep-cache. Reinstalling later needs no download."
        />
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="skip_hooks"
          alsoApply
          label="Skip hooks"
          hint="lmm --no-hooks. The list above updates to match."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="force"
          label="Force"
          hint="lmm uninstall --force. Carry on past a failure that would otherwise stop the flow."
        />
      <//>
    </div>
  `;
}
