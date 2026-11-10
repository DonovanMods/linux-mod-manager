// launchmodal.js - the Play button's answer to undeployed changes
// (issue 501).
//
// The launch was refused with core.LaunchDriftError's details: some
// enabled mods are not deployed, some disabled ones still are, or the
// game's mod_path needs attention. The modal says which, and offers the
// two ways on: Deploy & launch - the top bar's own deploy job, then the
// launch - when a deploy alone puts the profile in step (deploy_clears),
// and Launch anyway, which launches past the drift.
//
// A deploy is not a converge run: it leaves a disabled mod deployed, and it
// cannot fix a mod_path. Those causes are named with their own fix, and
// Deploy & launch is not offered for them.

import { html } from "../render.js";
import { Modal } from "./modal.js";
import { countOf } from "../modrows.js";

/** driftCauses is each reason the profile is out of step, with its fix -
 * one sentence per cause, the modal's body. */
export function driftCauses(drift) {
  const causes = [];
  const enabled = (drift.undeployed ?? 0) - (drift.still_deployed ?? 0);
  if (enabled > 0) {
    causes.push({
      key: "undeployed",
      text: `${countOf(enabled, "enabled mod")} ${enabled === 1 ? "isn't" : "aren't"} deployed.`,
    });
  }
  if (drift.still_deployed > 0) {
    causes.push({
      key: "still-deployed",
      text: `${countOf(drift.still_deployed, "disabled mod")} ${drift.still_deployed === 1 ? "is" : "are"} still deployed. A deploy leaves ${drift.still_deployed === 1 ? "it" : "them"} in place: Apply profile… on the Profile card takes ${drift.still_deployed === 1 ? "it" : "them"} down.`,
    });
  }
  if (drift.mod_path_error) {
    causes.push({
      key: "mod-path",
      text: `${drift.mod_path_error} Fix the mod path on the Setup page.`,
    });
  }
  return causes;
}

/** LaunchDriftModal is the choice: Deploy & launch, Launch anyway, or
 * Cancel. Self-guarded on the shared slot's shape, like every modal. */
export function LaunchDriftModal({ modal, actions }) {
  if (modal?.type !== "launch-drift") return null;
  const { drift, target } = modal;

  return html`
    <${Modal}
      kind="launch-drift"
      title=${`${target.name} has undeployed changes`}
      onClose=${actions.closeModal}
      footer=${html`
        <button
          type="button"
          class="button"
          data-action="cancel-launch"
          onClick=${actions.closeModal}
        >
          Cancel
        </button>
        <button
          type="button"
          class="button${drift.deploy_clears ? "" : " button--primary"}"
          data-action="launch-anyway"
          onClick=${actions.launchAnyway}
        >
          Launch anyway
        </button>
        ${
          drift.deploy_clears &&
          html`<button
            type="button"
            class="button button--primary"
            data-action="deploy-and-launch"
            onClick=${actions.deployAndLaunch}
          >
            Deploy & launch
          </button>`
        }
      `}
    >
      <p>The mods in the game folder don't match profile ${target.profile}:</p>
      <ul>
        ${driftCauses(drift).map(
          (c) => html`<li key=${c.key} data-cause=${c.key}>${c.text}</li>`,
        )}
      </ul>
    <//>
  `;
}
