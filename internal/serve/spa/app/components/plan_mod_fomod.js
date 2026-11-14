// plan_mod_fomod.js - the confirm modal's renderer for a
// core.FomodReconfigurePlan (kind_mod_fomod.go, `lmm mod fomod`, issue
// 615): "Reconfigure installer…" on the full mod page.
//
// There is nothing to choose here yet: Continue fetches the installed
// version again and the installer's questions follow in the wizard
// (fomodwizard.js), with the choices recorded now preselected.

import { html } from "../render.js";
import { displayVersion } from "../version.js";

/** recordedSummary words the recorded choices: each answered group's
 * picks, "Step > Group: A, B". */
function recordedSummary(choices) {
  const lines = [];
  for (const step of choices?.steps ?? []) {
    for (const group of step.groups ?? []) {
      const picks = (group.choices ?? []).map((c) => c.name).join(", ");
      lines.push(`${step.name} > ${group.name}: ${picks || "none"}`);
    }
  }
  return lines;
}

/**
 * InstallerPicker asks which installer of a mod whose files each carry one
 * (issue 634, plan.installers) the wizard asks again - one file's, or every
 * one - as `lmm mod fomod --file` does. It sets the job's file_id, an
 * apply-time option: the plan is the same either way.
 */
function InstallerPicker({ plan, modal, actions }) {
  const picked = modal?.applyOptions?.file_id ?? "";
  const pick = (fileID) => actions.setPlanOptions({ file_id: fileID });
  const disabled = modal?.status !== "ready";
  return html`
    <fieldset class="fomod__group" data-testid="fomod-installer-picker">
      <legend class="fomod__legend">
        Which installer
        <span class="fomod__rule"
          >This mod has an installer in each of its files</span
        >
      </legend>
      <label class="fomod__option" data-installer="">
        <input
          type="radio"
          name="fomod-installer"
          checked=${picked === ""}
          disabled=${disabled}
          onChange=${() => pick("")}
        />
        <span class="fomod__option-text">
          <span class="fomod__option-name">Every installer</span>
        </span>
      </label>
      ${plan.installers.map(
        (inst) => html`
          <label
            class="fomod__option"
            key=${inst.file_id}
            data-installer=${inst.file_id}
          >
            <input
              type="radio"
              name="fomod-installer"
              checked=${picked === inst.file_id}
              disabled=${disabled}
              onChange=${() => pick(inst.file_id)}
            />
            <span class="fomod__option-text">
              <span class="fomod__option-name"
                >${inst.name || `File ${inst.file_id}`}</span
              >
              <span class="fomod__description"
                >File ${inst.file_id}:
                ${recordedSummary(inst.choices).join("; ") || "no choices"}</span
              >
            </span>
          </label>
        `,
      )}
    </fieldset>
  `;
}

/** FomodReconfigurePlanView renders core.FomodReconfigurePlan. */
export function FomodReconfigurePlanView({ plan, modal, actions }) {
  if (plan.installers?.length > 1) {
    return html`
      <div class="plan plan--mod-fomod">
        <p class="plan__summary">
          lmm fetches${" "}
          <span class="mono">${plan.mod.name} ${displayVersion(plan.mod)}</span
          >${" "} again and asks the installer you pick, with your current
          choices selected. The other installers keep theirs.
        </p>
        <${InstallerPicker} plan=${plan} modal=${modal} actions=${actions} />
      </div>
    `;
  }
  const lines = recordedSummary(plan.choices);
  return html`
    <div class="plan plan--mod-fomod">
      <p class="plan__summary">
        lmm fetches${" "}
        <span class="mono">${plan.mod.name} ${displayVersion(plan.mod)}</span
        >${" "} again and asks its installer's questions, with your current
        choices selected. Your answers are installed and deployed in place of
        the current files.
      </p>
      ${
        lines.length > 0 &&
        html`<ul class="plan__mods" data-testid="fomod-recorded">
          ${lines.map((line) => html`<li key=${line}>${line}</li>`)}
        </ul>`
      }
    </div>
  `;
}
