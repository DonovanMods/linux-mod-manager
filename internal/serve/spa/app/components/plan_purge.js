// plan_purge.js - the confirm modal's renderer for a core.PurgePlan
// (kind_purge.go, `lmm purge`), C-3 of the epic live review: purge had no
// plan kind, no route and no control at all, in a UI whose design Scope
// claims full bidirectional parity.
//
// It is the most destructive single click in this application. `lmm purge`
// undeploys the whole profile; with --uninstall it also deletes every mod
// record behind it. So this renderer is the one that draws a
// type-the-profile-name gate, and confirmplan.js is what enforces it - the
// modal owns Confirm, and a renderer that owned its own disabled state
// would be asking the modal to trust it.
//
// plan.mods is the exact set the apply purges (internal/core/purge.go: "so
// the number shown and the number purged cannot disagree"), which is why
// the count in the sentence is read off the list rather than computed.

import { html } from "../render.js";
import { PlanAdvanced, PlanOption, ApplyOption } from "./planoptions.js";

/** PurgePlanView renders core.PurgePlan (internal/core/purge.go). */
export function PurgePlanView({ plan, modal, actions }) {
  const mods = plan.mods ?? [];
  const hooks = plan.hooks ?? [];
  // #269: the display names of the mods purge will NOT touch, so the shorter
  // mod count above has an explanation on the same screen.
  const external = plan.external ?? [];

  // The type-the-name gate renders on BOTH branches (MIN-4, the closing
  // wave's gate review). confirmplan.js's typedNameFor.purge demands
  // plan.profile back for this kind whatever the plan says, so an early
  // return without the input left Confirm permanently disabled with nothing
  // on screen explaining why. Harmless - there is nothing to purge - but
  // undocumented, and it would break outright the moment the input moved.
  const confirmName = html`
    <label class="plan__confirm-name">
      <span>Type <span class="mono">${plan.profile}</span> to confirm</span>
      <input
        type="text"
        name="purge-confirm"
        autocomplete="off"
        aria-label=${`Type ${plan.profile} to confirm the purge`}
        value=${modal?.confirmationText ?? ""}
        onInput=${(e) => actions.setPlanConfirmationText(e.currentTarget.value)}
      />
    </label>
  `;

  if (mods.length === 0) {
    return html`
      <div class="plan plan--purge">
        <p class="plan__note">
          ${
            external.length > 0
              ? html`Nothing to purge: profile${" "}
                  <span class="mono">${plan.profile}</span> holds only items
                  tracked from Steam, and lmm never touches their files.`
              : html`Nothing to purge: profile${" "}
                  <span class="mono">${plan.profile}</span> has no installed
                  mods.`
          }
        </p>
        ${confirmName}
      </div>
    `;
  }

  return html`
    <div class="plan plan--purge">
      <p class="plan__summary">
        This will undeploy ${mods.length} mod${mods.length === 1 ? "" : "s"}
        from profile${" "}
        <span class="mono">${plan.profile}</span>.
      </p>

      <p class="plan__note plan__note--warn" data-testid="purge-records-note">
        ${
          plan.uninstall
            ? "Mod records will also be removed from the database."
            : "Mod records will be preserved. Use Deploy to restore them."
        }
      </p>

      <section class="plan__section">
        <h3 class="plan__heading">Mods (${mods.length})</h3>
        <ul class="plan__mods">
          ${mods.map(
            (mod) => html`
              <li key=${`${mod.source_id}/${mod.id}`} class="plan__mod">
                <span class="plan__mod-name">${mod.name}</span>${" "}
                <span class="mono">${mod.version}</span>
              </li>
            `,
          )}
        </ul>
      </section>

      ${
        external.length > 0 &&
        html`
          <section class="plan__section" data-testid="purge-external">
            <h3 class="plan__heading">
              Left alone — tracked from Steam (${external.length})
            </h3>
            <ul class="plan__mods">
              ${external.map(
                (name) =>
                  html`<li key=${name} class="plan__mod">
                    <span class="plan__mod-name">${name}</span>
                  </li>`,
              )}
            </ul>
          </section>
        `
      }
      ${
        plan.merged_artifact &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading">Merged artifact</h3>
            <p class="mono">${plan.merged_artifact}</p>
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
      ${confirmName}

      <${PlanAdvanced}>
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="uninstall"
          alsoApply
          label="Also remove the mod records"
          hint="lmm purge --uninstall. The sentence above updates to match."
        />
        <${PlanOption}
          modal=${modal}
          actions=${actions}
          name="skip_hooks"
          alsoApply
          label="Skip hooks"
          hint="lmm --no-hooks."
        />
        <${ApplyOption}
          modal=${modal}
          actions=${actions}
          name="force"
          label="Force"
          hint="lmm purge --force. Continue even if hooks fail."
        />
      <//>
    </div>
  `;
}
