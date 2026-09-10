// plan_workshop_adopt.js - the confirm modal's renderer for a
// core.WorkshopAdoptPlan (issue 269, kind_workshop_adopt.go): "Track the Steam
// Workshop items you are subscribed to".
//
// It renders the same facts the CLI prints before its "Track these items?"
// prompt: what the scan found, one row per item that would be tracked with
// its revision date, and the ones Steam would not describe. Read-only -
// there is nothing to choose here beyond a refresh, which is a PLAN-time
// option.
//
// The leading note is the point of the whole preview: this job writes
// bookkeeping and touches no files. Saying that BEFORE the Confirm button
// is what stops "import" reading as "copy these into my game folder".

import { html } from "../render.js";

// TRACK_ONLY_NOTE is the sentence the whole preview exists for, kept as one
// string so it survives htm's whitespace collapsing intact (the trap
// cards.js#conflictLabel documents).
const TRACK_ONLY_NOTE =
  "lmm tracks these items where Steam put them. It never moves, copies or " +
  "deletes their files, and cannot deploy, enable or update them - Steam " +
  "does that itself.";

/**
 * WorkshopAdoptPlanView renders core.WorkshopAdoptPlan
 * (internal/core/workshop_adopt.go).
 */
export function WorkshopAdoptPlanView({ plan }) {
  const scan = plan.scan ?? {};
  const warnings = scan.warnings ?? [];
  const entries = plan.entries ?? [];

  return html`
    <div class="plan plan--workshop-adopt">
      <p class="plan__note">${TRACK_ONLY_NOTE}</p>

      <p class="plan__summary">${scanSummary(scan)}</p>

      ${warnings.map(
        (w) => html`<p key=${w} class="plan__note plan__note--warn">${w}</p>`,
      )}
      ${
        entries.length === 0
          ? html`<p class="plan__note">
              Every subscribed item is already tracked - there is nothing to do.
            </p>`
          : html`
              <section class="plan__section">
                <h3 class="plan__heading">To track (${entries.length})</h3>
                <ul class="plan__mods">
                  ${entries.map(
                    (e) => html`
                      <li key=${e.file_id}>
                        <span class="plan__mod-name">${entryName(e)}</span
                        >${" "}
                        <span class="plan__mod-detail">${entryDetail(e)}</span>
                      </li>
                    `,
                  )}
                </ul>
              </section>
            `
      }
    </div>
  `;
}

/**
 * scanSummary is the one-line "what the scan found" sentence, built as ONE
 * string rather than adjacent interpolations: htm collapses the whitespace
 * between those, which would run the numbers into the words either side of
 * them (the trap cards.js#conflictLabel documents).
 */
function scanSummary(scan) {
  const items = (scan.items ?? []).length;
  const libraries = (scan.libraries ?? []).length;
  const tracked = scan.tracked ?? 0;
  return (
    `${items} subscribed item${items === 1 ? "" : "s"} across ` +
    `${libraries} Steam librar${libraries === 1 ? "y" : "ies"}; ` +
    `${tracked} already tracked.`
  );
}

/** entryName is the item's Steam title, or an honest stand-in for one. */
function entryName(entry) {
  return entry.mod?.name || `Workshop item ${entry.file_id}`;
}

/**
 * entryDetail is one entry's own status line: the revision DATE, never the
 * 19-digit Steam content id the entry's manifest field carries.
 */
function entryDetail(entry) {
  if (entry.unavailable) {
    return entry.note || "Steam does not describe this item";
  }
  const date = revisionDate(entry.time_updated);
  return date ? `#${entry.file_id}, updated ${date}` : `#${entry.file_id}`;
}

/** revisionDate formats a Unix timestamp as a plain ISO date. */
function revisionDate(seconds) {
  if (!seconds) return "";
  return new Date(seconds * 1000).toISOString().slice(0, 10);
}
