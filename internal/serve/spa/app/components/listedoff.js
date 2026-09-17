// listedoff.js - a mod the profile lists, switched off, that was never
// downloaded (issue 440): core.ModList.disabled_not_installed.
//
// An imported profile on a fresh machine is the usual source: the document
// marks the mod `disabled: true`, and lmm never fetches a disabled mod, so
// there is no installed row for the library to show. These rows are the
// profile's all the same - listed, clearly off, clearly not downloaded -
// and the one thing that switches such a mod on is its download: an
// explicit install, which clears the marker (docs/configuration.md). So the
// row's action is Install…, through the same confirm-plan every install
// takes, and the row says what it will do.

import { html } from "../render.js";
import { displayVersion } from "../version.js";
import { InlineJob } from "./jobprogress.js";

/** listedOffRefs is the document's list, or an empty one. */
export function listedOffRefs(mods) {
  return mods?.disabled_not_installed ?? [];
}

/** refKey is domain.ModKey for a domain.ModReference. */
function refKey(ref) {
  return `${ref.source_id}:${ref.mod_id}`;
}

/** installOrigin is the row's own job origin - the same one a search
 * result or the slide-over uses for this mod, so wherever the install was
 * started, every control for it shows the one job. */
function installOrigin(ref) {
  return `install:${ref.source_id}/${ref.mod_id}`;
}

/**
 * ListedOffList renders the rows, or nothing when there are none. `heading`
 * is the section's title; the library and the Profile card each give their
 * own.
 */
export function ListedOffList({ refs, state, actions, heading }) {
  if (!refs || refs.length === 0) return null;

  function install(ref) {
    actions.openPlan({
      kind: "install",
      origin: installOrigin(ref),
      title: `Install ${refKey(ref)}`,
      confirmLabel: "Install and switch on",
      options: { source_id: ref.source_id, mod_id: ref.mod_id },
      // The version the profile lists is the one to install - the pick
      // travels as an apply option (kind_install.go), and the modal's
      // version picker starts on it.
      ...(ref.version ? { applyOptions: { version: ref.version } } : {}),
    });
  }

  return html`
    <div class="listed-off" data-testid="listed-off">
      ${heading && html`<h3 class="listed-off__heading">${heading}</h3>`}
      <ul class="listed-off__list">
        ${refs.map((ref) => {
          const version = displayVersion(ref);
          return html`
            <li
              key=${refKey(ref)}
              class="listed-off__row"
              data-mod=${refKey(ref)}
            >
              <span class="listed-off__name mono">${refKey(ref)}</span>
              ${version && html`<span class="mono">${version}</span>`}
              <span class="badge badge--policy">Off</span>
              <span class="badge badge--warn">Not downloaded</span>
              <span class="listed-off__hint"
                >Installing downloads it and switches it on.</span
              >
              <${InlineJob}
                origin=${installOrigin(ref)}
                state=${state}
                actions=${actions}
              >
                <button
                  type="button"
                  class="button button--small"
                  data-action="install-listed-off"
                  aria-label=${`Install ${refKey(ref)} and switch it on`}
                  onClick=${() => install(ref)}
                >
                  Install…
                </button>
              <//>
            </li>
          `;
        })}
      </ul>
    </div>
  `;
}
