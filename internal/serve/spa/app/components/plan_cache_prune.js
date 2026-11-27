// plan_cache_prune.js - the confirm modal's renderer for a
// core.CachePrunePlan (kind_cache_prune.go, `lmm cache prune`, issue 678):
// exactly what the prune removes - by game and mod, each version with its
// size - and the room it frees. plan.entries is the set the apply removes
// (it re-plans and refuses a plan that no longer matches), so the totals
// are read off the plan, never computed here.
//
// A mod nothing references (plan.uninstalled: not installed in any profile,
// named by no profile or snapshot) is left whole unless the user ticks
// "Also remove mods not installed anywhere" - a plan-time option, off by
// default, which re-plans (`lmm cache prune --include-uninstalled`).

import { html } from "../render.js";
import { formatBytes } from "../progress.js";
import { displayVersion } from "../version.js";
import { PlanOption } from "./planoptions.js";
import { oldVersions } from "./planprune.js";

/** keepSentence is the setting the plan applied, in words. */
export function keepSentence(keep) {
  if (keep < 0) return "The cache keeps every version (unlimited).";
  const previous =
    keep === 1 ? "1 previous version" : `${keep} previous versions`;
  return `Each mod keeps its installed versions, the newest ${previous}, and every version a profile or snapshot references.`;
}

/** uninstalledBytes is the room the plan's uninstalled mods take. */
function uninstalledBytes(plan) {
  return (plan.uninstalled ?? []).reduce((n, m) => n + (m.bytes ?? 0), 0);
}

/** UninstalledOption is the opt-in for the mods nothing references, with
 * what they take; nothing when the plan found none. */
function UninstalledOption({ plan, modal, actions }) {
  const mods = plan.uninstalled ?? [];
  if (mods.length === 0) return null;
  const count = mods.length === 1 ? "1 mod" : `${mods.length} mods`;
  return html`<div data-testid="cache-prune-uninstalled">
    <${PlanOption}
      modal=${modal}
      actions=${actions}
      name="include_uninstalled"
      label="Also remove mods not installed anywhere"
      hint=${`${count} no profile or snapshot uses (${formatBytes(uninstalledBytes(plan)) || "0 B"}), such as one uninstalled with its cache kept. They are kept unless you tick this.`}
    />
  </div>`;
}

/** CachePrunePlanView renders core.CachePrunePlan. */
export function CachePrunePlanView({ plan, modal, actions }) {
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
        <${UninstalledOption} plan=${plan} modal=${modal} actions=${actions} />
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
      <${UninstalledOption} plan=${plan} modal=${modal} actions=${actions} />
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
                ${formatBytes(e.bytes) || "0 B"}${e.variant ? ` - variant ${e.variant}` : ""}${e.uninstalled ? " - not installed anywhere" : ""}${plan.games?.length > 1 ? ` - ${e.game_id}` : ""}
              </span>
            </li>`,
        )}
      </ul>
    </div>
  `;
}
