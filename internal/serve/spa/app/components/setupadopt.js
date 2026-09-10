// setupadopt.js - the Setup page's Adopt section (issue 333, kind_adopt.go):
// "Import untracked mods already in the game folder". A single control -
// the plan itself IS the scan preview (core.AdoptPlan carries the whole
// LocalScan), so there is nothing to fetch before opening the confirm-plan
// modal. Wrapped in InlineJob the same way setupimport.js's own control is,
// for the identical completion-affordance reason (unit7-carry.md N7).
//
// Issue 348 (design Q3) adds a SECOND card after it, on the same principle
// and for a game mapped to the steamworkshop source: `lmm import
// --workshop` is a pure local read of what the Steam client has already
// downloaded, which makes it the highest-value zero-cost discovery a
// Workshop-native game has - and this section, reached straight after
// first-run detect, is where a new user looks for "what do I already
// have?". It reuses the same plan kind, renderer and confirm modal the
// library's Add mods ▾ menu opens, so there is one flow with two entry
// points rather than two flows.
//
// The Workshop card is offered only when the game actually maps the
// steamworkshop source - the mapping `lmm game detect` writes for a game
// whose appworkshop manifest declares installed items - because the plan
// endpoint answers 400 otherwise, and a control that can only fail is worse
// than no control (addmodsmenu.js makes the same call from the same fact).

import { html, useState } from "../render.js";
import { InlineJob } from "./jobprogress.js";

const ADOPT_ORIGIN = "setup:adopt";
const WORKSHOP_ORIGIN = "setup:workshop-adopt";

export function SetupAdopt({ state, actions }) {
  const [skipMatch, setSkipMatch] = useState(false);
  const workshopMapped = Boolean(state?.status?.source_ids?.steamworkshop);

  function start() {
    actions.openPlan({
      kind: "adopt",
      origin: ADOPT_ORIGIN,
      title: "Import untracked mods",
      confirmLabel: "Adopt",
      options: { skip_match: skipMatch },
    });
  }

  function startWorkshop() {
    actions.openPlan({
      kind: "workshop_adopt",
      origin: WORKSHOP_ORIGIN,
      title: "Track Steam Workshop items",
      confirmLabel: "Track",
      options: {},
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
    ${
      workshopMapped &&
      html`
        <div class="setup-section" data-testid="setup-workshop-adopt">
          <h3 class="plan__heading">Steam Workshop</h3>
          <p class="empty-state__hint">
            Reads what the Steam client has already downloaded for this game and
            records the items lmm doesn't track yet. Nothing is downloaded,
            moved or copied - Steam owns those files where they sit, and the
            game loads them from there.
          </p>
          <div class="setup-section__actions">
            <${InlineJob}
              origin=${WORKSHOP_ORIGIN}
              state=${state}
              actions=${actions}
            >
              <button
                type="button"
                class="button button--primary button--small"
                data-action="plan-workshop-adopt"
                onClick=${startWorkshop}
              >
                Scan Steam Workshop…
              </button>
            <//>
          </div>
        </div>
      `
    }
  `;
}
