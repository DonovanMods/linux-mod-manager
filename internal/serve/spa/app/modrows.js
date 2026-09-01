// modrows.js - pure library-table logic: joining the four /api/v1 documents
// Mission Control's library reads into one row per mod, plus the filtering,
// sorting and undeployed-change counting the top bar and library controls
// need (docs/plans/2026-08-31-serve-spa-design.md §Mission Control:
// "Library"). Extracted from the rendering component so it has no DOM to
// drive - the SPA has no JS test runner (no Node anywhere), so this is
// exercised through the library component's chromedp scenarios, but keeping
// the join and the predicates pure is what makes that coverage mean
// anything: a rendering bug and a join bug cannot hide behind each other.

/** The wire key a mod is addressed by everywhere but the URL (domain.ModKey:
 * "sourceID:modID") - core.ConflictModRef.Key and every /api/v1/updates row
 * key off it the same way. */
export function modKey(mod) {
  return `${mod.source_id}:${mod.id}`;
}

/**
 * Builds one library row per installed mod (mods: core.ModList's own "mods"
 * array, already in the profile's load order): the ModListing fields
 * verbatim, plus the derived badges no single document carries alone,
 * cross-referenced by the same keys their owning documents use.
 *
 *   - updates: core.UpdateCheckReport's "updates" array (domain.Update),
 *     keyed by its embedded installed_mod.
 *   - findings: core.VerifyResult's "findings" array (core.VerifyFinding),
 *     matched by mod_id - the wire format carries no source, so within one
 *     profile this assumes mod IDs don't collide across sources, the same
 *     assumption the finding itself already makes.
 *   - conflicts: core.ConflictReport's "conflicts" array (core.
 *     ProfileConflict), matched by the owner/also_in ConflictModRef keys.
 */
export function buildRows(mods, updates, findings, conflicts) {
  const updateByKey = new Map(
    (updates ?? []).map((u) => [modKey(u.installed_mod), u]),
  );
  const unhealthyIDs = new Set(
    (findings ?? []).filter((f) => f.status !== "ok").map((f) => f.mod_id),
  );
  const conflictKeys = new Set();
  for (const c of conflicts ?? []) {
    conflictKeys.add(c.owner.key);
    for (const also of c.also_in) conflictKeys.add(also.key);
  }

  return (mods ?? []).map((mod, index) => {
    const key = modKey(mod);
    const update = updateByKey.get(key);
    return {
      ...mod,
      key,
      // index + 1 is a DISPLAY index, not the profile's actual load order:
      // ListMods (core/queries.go) places a mod that's installed but absent
      // from the profile's load order first, so those rows are labelled 1,
      // 2, … under a "Load order" column when they have none. Won't-fix for
      // now - ModListing carries no field distinguishing "has no load-order
      // entry" on the wire, and adding one is the same kind of JSON-contract
      // change this unit's gate keeps frozen (see cards.js's HealthCard
      // comment for the sibling case).
      loadOrder: index + 1,
      hasUpdate: Boolean(update),
      updateTarget: update?.new_version ?? "",
      hasHealthIssue: unhealthyIDs.has(mod.id),
      hasConflict: conflictKeys.has(key),
    };
  });
}

// filterPredicates backs the library's filter control. "load-order" has no
// entry in SORT_PREDICATES below for the identical reason: it is the input
// order, not a comparator.
const filterPredicates = {
  all: () => true,
  enabled: (row) => row.enabled,
  updatable: (row) => row.hasUpdate,
  unhealthy: (row) => row.hasHealthIssue,
};

/** The filter control's options, in display order. */
export const FILTER_NAMES = Object.keys(filterPredicates);

/** Applies the named filter (an unknown name behaves as "all"). */
export function filterRows(rows, filter) {
  return rows.filter(filterPredicates[filter] ?? filterPredicates.all);
}

// sortComparators backs the library's sort control. "recent" orders by
// InstalledAt descending - the wire's own name for the moment a mod joined
// the library - matching the design doc's "recently-updated" name for what
// this control moves in a local install's own timeline.
const sortComparators = {
  name: (a, b) => a.name.localeCompare(b.name),
  // Date.parse(...) || 0 rather than bare `new Date(...) - new Date(...)`:
  // an absent or unparsable installed_at parses to NaN, and Array.sort with
  // a NaN comparator result gives an arbitrary order - falling back to the
  // epoch keeps every undated row sorted (last, under "descending").
  recent: (a, b) =>
    (Date.parse(b.installed_at) || 0) - (Date.parse(a.installed_at) || 0),
};

/** The sort control's options, in display order. */
export const SORT_NAMES = ["load-order", ...Object.keys(sortComparators)];

/** Applies the named sort; "load-order" (and any unknown name) is a no-op -
 * the rows already arrived in load order. */
export function sortRows(rows, sort) {
  const cmp = sortComparators[sort];
  return cmp ? [...rows].sort(cmp) : rows;
}

/**
 * Counts installed mods whose desired state (Enabled) disagrees with what is
 * actually on disk (Deployed) - the top bar's undeployed-changes indicator.
 * Both fields live on every ModListing already; nothing else needs fetching
 * to answer this.
 */
export function countUndeployed(mods) {
  return (mods ?? []).reduce(
    (n, m) => n + (m.enabled !== m.deployed ? 1 : 0),
    0,
  );
}

// modOriginPattern parses the "mod:{source}/{id}:{action}" origin
// convention every per-mod control this unit adds uses (jobprogress.js's
// own doc comment names this exact shape). sourceID stops at the FIRST "/"
// (a registry key never contains one); modID is everything up to the LAST
// ":" (router.js's own ?mod= parsing tolerates a modID that itself contains
// "/", so this must too - a greedy `.+` backtracks to let the trailing
// `:action` anchor win).
//
// The versions table's own origin(`update:${v}`) (fullmodpage.js) is a
// DIFFERENT shape - "action:version", not a bare action - so its trailing
// ":${v}" is meant to fall outside `[a-z_]+` and never match here at all
// (M3, harmless: that table's own controls are never looked up through
// this map). It only holds for a NUMERIC v ("update:2.0" has a "." the
// action group can't consume). A purely-lowercase v ("update:beta") WOULD
// match: `[a-z_]+` claims "beta" as the action and modID's own greedy
// `(.+)` absorbs "a:update" instead, keying a row that never exists. No
// source in this fixture set reports a non-numeric version, so this stays
// theoretical - flagged rather than fixed, since a real one would need a
// less ambiguous origin shape for that table, not a smarter regex here.
const modOriginPattern = /^mod:([^/]+)\/(.+):([a-z_]+)$/;

/**
 * runningMutations maps each mod currently being mutated - as far as THIS
 * browser session's own controls can say - to that job's summary/progress
 * frame, for the library's own row-level live indicator (issue 330 carry-3:
 * "say WHICH mutation, or move onto the rows it concerns"). It reads
 * state.origins (jobprogress.js's control -> job id map), not the job's own
 * document: the registry attributes a job to no mod at all (activity.go's
 * jobSummary carries no such field), so only the control that started it -
 * here, the row itself - can say which mod a running job belongs to. A job
 * started from another tab or script therefore never appears here; Mission
 * Control's header falls back to naming the kind alone for that case
 * (missioncontrol.js).
 */
export function runningMutations(jobsIndex, jobProgress, origins) {
  const byID = new Map((jobsIndex ?? []).map((j) => [j.id, j]));
  const result = new Map();
  for (const [origin, jobID] of Object.entries(origins ?? {})) {
    const match = modOriginPattern.exec(origin);
    if (!match) continue;
    const summary = byID.get(jobID);
    if (!summary || summary.state !== "running") continue;
    const [, sourceID, modID] = match;
    result.set(`${sourceID}:${modID}`, {
      jobID,
      summary,
      frame: jobProgress?.[jobID],
    });
  }
  return result;
}

/**
 * Renders an ISO timestamp as a short local date for the library's
 * "Installed" column, or an em dash when there is nothing parsable to
 * render. The dash matters: a blank cell under a heading reads as a
 * rendering bug, while "—" says the document carries no date - which is a
 * real state (an adopted mod whose installed_at was never recorded).
 */
export function formatDate(value) {
  const ms = Date.parse(value);
  if (Number.isNaN(ms)) return "—";
  return new Date(ms).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}
