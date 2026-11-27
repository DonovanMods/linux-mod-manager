// plan_cache_prune.js - the confirm modal's renderer for a
// core.CachePrunePlan (kind_cache_prune.go, `lmm cache prune`, issue 678):
// exactly what the prune removes - by game and mod, each version with its
// size - and the room it frees. plan.entries is the set the apply removes
// (it re-plans and refuses a plan that no longer matches), so the totals
// are read off the plan, never computed here.

import { html } from "../render.js";
import { formatBytes } from "../progress.js";
import { displayVersion } from "../version.js";
import { oldVersions } from "./planprune.js";

/** keepSentence is the setting the plan applied, in words. */
export function keepSentence(keep) {
  if (keep < 0) return "The cache keeps every version (unlimited).";
  const previous =
    keep === 1 ? "1 previous version" : `${keep} previous versions`;
  return `Each mod keeps its installed versions, the newest ${previous}, and every version a profile or snapshot references.`;
}

/** CachePrunePlanView renders core.CachePrunePlan. */
export function CachePrunePlanView({ plan }) {
  const entries = plan.entries ?? [];
  const skipped = plan.skipped ?? [];
  const skippedNotes = skipped.map(
    (s) =>
      html`<p class="plan__note plan__note--warn" key=${s.game_id}>
        ${s.game_id}'s cache is left whole: ${s.reason}
      </p>`,
  );
  if (entries.length === 0) {
    return html`
      <div class="plan plan--cache-prune" data-testid="cache-prune-plan">
        <p class="plan__note" data-testid="cache-prune-nothing">
          Nothing to prune. ${keepSentence(plan.keep_previous_versions)}
        </p>
        ${skippedNotes}
      </div>
    `;
  }
  return html`
    <div class="plan plan--cache-prune" data-testid="cache-prune-plan">
      <p class="plan__summary" data-testid="cache-prune-summary">
        This removes ${oldVersions(plan.versions)} from the cache, freeing${" "}
        ${formatBytes(plan.bytes) || "0 B"}.
      </p>
      <p class="plan__note">${keepSentence(plan.keep_previous_versions)}</p>
      ${skippedNotes}
      <ul class="plan__mods">
        ${entries.map(
          (e) =>
            html`<li
              class="plan__mod"
              key=${`${e.game_id}/${e.source_id}:${e.mod_id}/${displayVersion(e)}@${e.variant ?? ""}`}
              data-testid="cache-prune-entry"
            >
              <span class="plan__mod-name"
                >${e.mod_name || `${e.source_id}:${e.mod_id}`}</span
              >${" "}
              <span class="plan__mod-version mono">${displayVersion(e)}</span
              >${" "}
              <span class="plan__mod-detail">
                ${formatBytes(e.bytes) || "0 B"}${e.variant ? ` - variant ${e.variant}` : ""}${plan.games?.length > 1 ? ` - ${e.game_id}` : ""}
              </span>
            </li>`,
        )}
      </ul>
    </div>
  `;
}
