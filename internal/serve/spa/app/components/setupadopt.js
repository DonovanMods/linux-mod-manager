// setupadopt.js - the Setup page's Adopt section (issue 333, kind_adopt.go):
// "Import untracked mods already in the game folder". A single control -
// the plan itself IS the scan preview (core.AdoptPlan carries the whole
// LocalScan), so there is nothing to fetch before opening the confirm-plan
// modal. Wrapped in InlineJob the same way setupimport.js's own control is,
// for the identical completion-affordance reason (unit7-carry.md N7).

import { html, useState } from "../render.js";
import { InlineJob } from "./jobprogress.js";

const ADOPT_ORIGIN = "setup:adopt";

export function SetupAdopt({ state, actions }) {
  const [skipMatch, setSkipMatch] = useState(false);

  function start() {
    actions.openPlan({
      kind: "adopt",
      origin: ADOPT_ORIGIN,
      title: "Import untracked mods",
      confirmLabel: "Adopt",
      options: { skip_match: skipMatch },
    });
  }

  return html`
    <div class="setup-section" data-testid="setup-adopt">
      <p class="empty-state__hint">
        Scans the game's mod directory for files lmm doesn't already track and
        offers to bring them under management.
      </p>
      <label class="plan__control plan__control--inline">
        <input
          type="checkbox"
          checked=${skipMatch}
          onChange=${(e) => setSkipMatch(e.currentTarget.checked)}
        />
        Skip source matching (faster, no metadata lookup)
      </label>
      <div class="setup-section__actions">
        <${InlineJob} origin=${ADOPT_ORIGIN} state=${state} actions=${actions}>
          <button
            type="button"
            class="button button--primary button--small"
            data-action="plan-adopt"
            onClick=${start}
          >
            Scan for untracked mods…
          </button>
        <//>
      </div>
    </div>
  `;
}
