// planprune.js - the line a flow's plan carries about the cache prune that
// follows it (issue 678): an update, a rollback, a profile apply or switch
// and a collection update each prune the old cached versions of the mods
// they change, and their plan's `cache_prune` (core.CachePruneSummary)
// says what that frees, as the CLI's "Cache: pruning afterwards frees ..."
// line does. Nothing when it frees nothing.

import { html } from "../render.js";
import { formatBytes } from "../progress.js";

/** oldVersions is "1 old version" or "n old versions". */
export function oldVersions(n) {
  return n === 1 ? "1 old version" : `${n} old versions`;
}

/** PlanCachePrune renders a plan's `cache_prune`, or nothing. */
export function PlanCachePrune({ summary, testID = "plan-cache-prune" }) {
  if (!summary || !(summary.versions > 0)) return null;
  return html`<p class="plan__note" data-testid=${testID}>
    Cache: pruning afterwards frees ${formatBytes(summary.bytes) || "0 B"}
    (${oldVersions(summary.versions)}).
  </p>`;
}
