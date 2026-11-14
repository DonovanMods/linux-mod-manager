// verify.js - small, pure helpers shared by every surface that renders a
// core.VerifyFinding: the Health card's own list (cards.js), the verify_fix
// confirm modal's preview (plan_verify_fix.js, issue 332) and the
// slide-over's Findings section (modpanel.js). Extracted rather than
// duplicated so the surfaces can never drift on what a finding's own words
// say - which they had, until issue 334's gate review found the slide-over
// still printing the raw status slug beside a card rendering the same
// finding as prose.

import { isGameRoot, isPrefixRoot } from "./deploypath.js";
import { displayVersion } from "./version.js";

/** findingSubject names what a finding is about: its mod, or - for a
 * game-level row with no mod (issue 460: `mod_path_missing`) - the game's
 * mod path, (issue 605: `prefix_unresolved`) its Wine/Proton prefix, or
 * (issue 616) its plugin list. */
export function findingSubject(f) {
  if (f.status === "mod_path_missing") return "Mod path";
  if (f.status === "prefix_unresolved") return "Wine/Proton prefix";
  // issue 616: the plugin list's rows are the game's Plugins.txt, never one
  // mod's; the note names the plugin.
  if (isPluginListFinding(f)) return "Plugin list";
  // issue 620: likewise the game's load-order list (modlist.txt).
  if (isLoadOrderFinding(f)) return "Load order";
  return f.mod_name || f.mod_id || f.status.replaceAll("_", " ");
}

/** isPluginListFinding reports whether a finding is one of the plugin
 * list's own rows (issue 616): a sort finding, drift, or its repair. */
export function isPluginListFinding(f) {
  return (
    typeof f?.status === "string" &&
    (f.status.startsWith("plugin_") || f.status === "fixed_plugin_drift")
  );
}

/** isLoadOrderFinding reports whether a finding is one of a file load-order
 * list's own rows (issue 620): a sync finding, drift, or its repair. */
export function isLoadOrderFinding(f) {
  return (
    typeof f?.status === "string" &&
    (f.status.startsWith("load_order_") ||
      f.status === "fixed_load_order_drift")
  );
}

/** findingLabel prefers a finding's own note (already human-worded, e.g. a
 * repair failure's reason) and falls back to the status verbatim, appending
 * the recorded/source versions VerifyFinding carries for a version_mismatch
 * - the most common finding - to match the CLI's own "AlphaMod - VERSION
 * MISMATCH (recorded 1.0, source reports 2.0)". */
export function findingLabel(f) {
  const label = f.note || f.status.replaceAll("_", " ");
  // issue 590: a mod gone from its catalog names its likely new id, or
  // every candidate when there are several - the Updates card's words.
  const successors = f.successors ?? [];
  if (successors.length === 1) {
    return `${label} - looks like it moved to ${successors[0].mod_id} (same name and author)`;
  }
  if (successors.length > 1) {
    return `${label} - several mods have the same name and author: ${successorList(successors)}`;
  }
  if (f.recorded && f.effective) {
    return `${label} (recorded ${f.recorded}, source reports ${f.effective})`;
  }
  return label;
}

/** isGameRootFinding reports whether a finding is about a file deployed
 * under the game root (issue 629) - core's root field, never a guess from
 * the path - so a row can say so beside its path. */
export function isGameRootFinding(f) {
  return isGameRoot(f?.root);
}

/** isPrefixFinding reports whether a finding is about a file deployed
 * under the game's Wine/Proton prefix (issue 636) - core's root field. */
export function isPrefixFinding(f) {
  return isPrefixRoot(f?.root);
}

/** isInformational reports whether a finding is a fact about the install
 * rather than a problem with it (issue 630): a mod file the profile's
 * overrides replace ("deployed_overridden") is the profile's own choice.
 * It is shown wherever findings are, and counted nowhere a problem is. */
export function isInformational(f) {
  return f?.status === "deployed_overridden";
}

/** isRepaired reports whether a --fix run repaired this finding (issue 517).
 *
 * The "fixed_*" statuses say so themselves. A repair that resolves a row to
 * "ok" cannot - a repaired "ok" and an untouched one are the same status - so
 * core marks it with the repair's own sentence in `repair`. */
export function isRepaired(f) {
  if (typeof f?.status !== "string") return false;
  return (
    f.status.startsWith("fixed_") || (f.status === "ok" && Boolean(f.repair))
  );
}

/** repairOutcome splits a finished --fix run's findings into the two groups
 * worth reading: what it repaired, and what still needs attention. Every
 * other row - a healthy file, a skipped one - is neither, and is left out
 * so the list stays about the repair rather than about the profile. */
export function repairOutcome(findings) {
  const repaired = [];
  const attention = [];
  for (const f of Array.isArray(findings) ? findings : []) {
    if (isRepaired(f)) repaired.push(f);
    else if (
      f?.status &&
      f.status !== "ok" &&
      f.status !== "skipped" &&
      !isInformational(f)
    ) {
      attention.push(f);
    }
  }
  return { repaired, attention };
}

/** repairNote is the sentence saying what a repair did: the row's own
 * `repair`, or - for a "fixed_*" row - its note. */
export function repairNote(f) {
  return f.repair || f.note || "";
}

/** successorList names several likely new ids of a mod gone from its
 * catalog (core.CatalogSuccessor, issue 590), each with its version. */
export function successorList(successors) {
  return successors
    .map((c) => {
      const version = displayVersion(c);
      return version ? `${c.mod_id} (${version})` : c.mod_id;
    })
    .join(", ");
}
