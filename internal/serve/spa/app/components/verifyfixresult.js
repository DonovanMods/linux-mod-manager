// verifyfixresult.js - what a finished verify --fix job did (issue 517).
//
// The run's result is every file it checked, and nearly all of them are
// healthy. This lists only the two groups a person came to read: what is
// still wrong, first, and what the run repaired. A remaining download
// failure carries its mod's page so the file can be fetched by hand.
//
// Status is stated in words on every row ("Still failing", "Repaired"); the
// colour only reinforces it.

import { html } from "../render.js";
import { findingLabel, findingSubject, repairNote } from "../verify.js";
import { ModPageLink } from "./modpagelink.js";

// maxRows caps one group's list. A profile-wide repair can leave hundreds of
// rows failing for one shared reason; the count in the heading says how many,
// and the Health card lists them all.
const maxRows = 25;

/**
 * VerifyFixResult renders a verify_fix job's outcome as a labelled region
 * with a heading per group, or nothing when `outcome` is null.
 */
export function VerifyFixResult({ outcome }) {
  if (!outcome) return null;
  return html`
    <section
      class="verify-result"
      data-testid="verify-fix-result"
      aria-label="Repair results"
    >
      <${Group}
        group="attention"
        title="Still needs attention"
        status="Still failing"
        findings=${outcome.attention}
      />
      <${Group}
        group="repaired"
        title="Repaired"
        status="Repaired"
        findings=${outcome.repaired}
      />
    </section>
  `;
}

function Group({ group, title, status, findings }) {
  if (findings.length === 0) return null;
  const shown = findings.slice(0, maxRows);
  return html`
    <div class="verify-result__group" data-group=${group}>
      <h4 class="verify-result__heading">${`${title} (${findings.length})`}</h4>
      <ul class="verify-result__list">
        ${shown.map(
          (f, i) => html`
            <li
              key=${`${f.mod_id}/${f.file_id}/${i}`}
              class="verify-result__row"
              data-mod=${f.mod_id ?? ""}
            >
              <span
                class="verify-result__status verify-result__status--${group}"
                >${status}</span
              >${" "}
              <span class="verify-result__mod">${findingSubject(f)}</span>${" "}
              <span class="verify-result__note"
                >${group === "repaired" ? repairNote(f) : findingLabel(f)}</span
              >
              <${ModPageLink}
                url=${f.mod_url}
                sourceID=${f.source_id}
                modName=${f.mod_name}
              />
            </li>
          `,
        )}
      </ul>
      ${
        findings.length > shown.length &&
        html`<p class="verify-result__more">
          ${`…and ${findings.length - shown.length} more.`}
        </p>`
      }
    </div>
  `;
}
