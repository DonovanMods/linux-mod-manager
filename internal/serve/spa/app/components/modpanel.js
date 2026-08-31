// modpanel.js - the ?mod= slide-over's PLACEHOLDER (docs/plans/2026-08-31-
// webui-impl.md Unit 2: "render a placeholder panel that names it"). The
// real slide-over - name, author, versions, changelog, findings, key
// actions - is Unit 4's; this unit only proves the URL annotation, the open/
// close wiring and the panel's screen real estate are all in place.

import { html } from "../render.js";
import { navigate } from "../router.js";

/** modKey is "source/id" (the URL's own separator - see router.js), not
 * domain.ModKey's "source:id" wire form; ModPanel only ever reads it back
 * out of the URL it was written to. */
export function ModPanel({ modKey, contextPath }) {
  const [sourceID, modID] = (modKey ?? "").split("/", 2);

  function close() {
    navigate(contextPath);
  }

  return html`
    <div class="slide-over" role="dialog" aria-label="Mod details">
      <div class="slide-over__panel">
        <button
          type="button"
          class="slide-over__close"
          onClick=${close}
          aria-label="Close"
        >
          ×
        </button>
        <p class="section-header">Slide-over</p>
        <p class="mono">${sourceID} / ${modID}</p>
        <p class="placeholder-note">
          The full drill-in - name, author, versions, changelog, findings, its
          key actions, and Esc/outside-click/arrow-key navigation - lands in
          Unit 4. This placeholder only proves the URL annotation and the close
          button.
        </p>
      </div>
    </div>
  `;
}
