// plan_collection_finish.js - the confirm modal's renderer for a
// core.CollectionFinishPlan (kind_collection_finish.go, `lmm profile
// finish`, issue 660): which of a collection profile's items left to finish
// this finish installs, and how - as the collection pins it, at the mod's
// current file in place of a gone one, or without a gone secondary file -
// which it skips, and how many of the downloads it needs an earlier attempt
// kept, so nothing is downloaded again for them.

import { html } from "../render.js";
import { displayVersion } from "../version.js";

/** todoName is an item's name, or its source:mod when the import had none. */
export function todoName(t) {
  return t?.name || `${t?.source_id}:${t?.mod_id}`;
}

/** downloadedFiles is "1 downloaded file" or "N downloaded files". */
export function downloadedFiles(n) {
  return n === 1 ? "1 downloaded file" : `${n} downloaded files`;
}

/** choiceText says how an item installs (core.CollectionFinishItem). */
function choiceText(item) {
  switch (item.choice) {
    case "use_current": {
      const current = item.current ?? {};
      const version = displayVersion(current);
      const name = current.name || `file ${current.file_id ?? ""}`.trim();
      return `the current file instead: ${name}${version ? ` ${version}` : ""}`;
    }
    case "skip_file":
      return "without the file the source no longer offers";
    default:
      return item.todo?.reason === "installer_questions"
        ? "its installer asks; answer it in the next step"
        : "as the collection pins it";
  }
}

/** CollectionFinishPlanView renders core.CollectionFinishPlan. */
export function CollectionFinishPlanView({ plan }) {
  const install = plan.install ?? [];
  const skip = plan.skip ?? [];
  const left = (plan.todos ?? []).length - install.length - skip.length;
  return html`
    <div class="plan plan--collection-finish" data-testid="finish-plan">
      <p class="plan__summary">
        ${
          plan.no_changes
            ? html`Nothing to finish in${" "}
                <span class="mono">${plan.profile}</span>.`
            : html`Finishing ${install.length + skip.length} of${" "}
                ${(plan.todos ?? []).length} item(s) left in${" "}
                <span class="mono">${plan.profile}</span>; each goes at its
                place in the load order.`
        }
        ${
          plan.recorded_only &&
          html` ${plan.profile} is not the active profile, so the mods are
          recorded in it and deployed when you switch to it.`
        }
      </p>
      ${
        (plan.reusable_downloads ?? 0) > 0 &&
        html`<p class="plan__note" data-testid="finish-reused">
          Reusing ${downloadedFiles(plan.reusable_downloads)} an earlier attempt
          kept: nothing is downloaded again for
          ${plan.reusable_downloads === 1 ? " it" : " them"}.
        </p>`
      }
      ${
        install.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">To install (${install.length})</h3>
          <ul class="plan__mods" data-testid="finish-install">
            ${install.map((item) => {
              const key = `${item.todo?.source_id}:${item.todo?.mod_id}`;
              return html`<li key=${key} class="plan__mod" data-finish=${key}>
                <span class="plan__mod-name">${todoName(item.todo)}</span
                >:${" "}
                <span class="plan__mod-detail"
                  >${choiceText(item)}${
                    (item.reused ?? 0) > 0
                      ? ` (reusing ${downloadedFiles(item.reused)})`
                      : ""
                  }</span
                >
              </li>`;
            })}
          </ul>
        </section>`
      }
      ${
        skip.length > 0 &&
        html`<section class="plan__section">
          <h3 class="plan__heading">To skip (${skip.length})</h3>
          <ul class="plan__mods" data-testid="finish-skip">
            ${skip.map((t) => {
              const key = `${t.source_id}:${t.mod_id}`;
              return html`<li key=${key} class="plan__mod" data-finish=${key}>
                <span class="plan__mod-name">${todoName(t)}</span>
              </li>`;
            })}
          </ul>
          <p class="plan__note">
            A skipped item leaves the profile, and a collection update does not
            add it back.
          </p>
        </section>`
      }
      ${
        left > 0 &&
        html`<p class="plan__note">
          ${left} other item(s) stay on the list to finish later.
        </p>`
      }
    </div>
  `;
}
