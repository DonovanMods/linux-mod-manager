// addapikey.js - "Add API key…": the way out of a source that refused for
// want of a key (issue 582).
//
// The usual cause is a key set only in a login shell: `lmm serve` under its
// systemd user unit does not inherit the shell's exports, so the source has
// no key there even though the CLI works. The fix is a stored key, and the
// web UI's place for one is Setup > Authentication - this button goes
// there, to that source's own row (router.js#authSetupPath). One component
// for every place a refusal is shown (the health card, the updates card,
// search warnings), matching the "Set mod path…" action's idiom.

import { html } from "../render.js";
import { authSetupPath, navigate } from "../router.js";
import { useSourceName } from "../sourcenames.js";

/**
 * AddAPIKeyButton links sourceID to its key field. `state` supplies the
 * game/profile the Setup URL hangs off. The accessible name names the
 * source, so a list of these says which key each one is for.
 */
export function AddAPIKeyButton({ state, sourceID }) {
  const sourceName = useSourceName(sourceID);
  const { game, profile } = state.route;
  return html`<button
    type="button"
    class="button button--small"
    data-action="add-api-key"
    data-source=${sourceID}
    aria-label=${`Add API key for ${sourceName}…`}
    onClick=${() => navigate(authSetupPath(game, profile, sourceID))}
  >
    Add API key…
  </button>`;
}
