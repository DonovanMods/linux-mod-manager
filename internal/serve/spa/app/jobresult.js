// jobresult.js - fetching a finished job's own RESULT document for the
// details a readout shows beneath its outcome: a switch's warnings, a
// purge's or a repair's own outcome, a batch's failed items.
//
// jobSummary (activity.go's own doc comment: "jobStatus without Result...
// fifty result documents in one poll would make the cheapest request in
// the application the most expensive one") never carries a job's Result -
// so a control that wants those details has to make its OWN read of the
// full document (api.js#jobStatus), same as jobhistory.js already does for
// a different reason. The outcome TALLY ("n applied / m failed") is not
// one of them: it is on the summary itself (activity.go's jobSummary.Tally,
// issue 364), because a label that waited for this second read said "Done"
// for the frames in between. Kept out of progress.js on purpose: that
// module stays pure and fetch-free, rendering only what it is handed.
import { useEffect, useState } from "./render.js";
import { jobStatus } from "./api.js";
import { resultTallyTone } from "./progress.js";
import { repairOutcome } from "./verify.js";
import { batchFailuresFor } from "./failures.js";

// readCache is a page-lifetime Map from job id to its own (possibly null)
// read of the result - readResult's shape - so the SAME finished job
// showing in two places at once (a row's own InlineJob AND the tray's entry
// for it) fetches its result exactly once, and neither ever re-fetches on a
// later re-render.
const readCache = new Map();

// pendingReads is a job id's in-flight read, while one is outstanding.
// useJobResultWarnings and useJobResultPurge are two SEPARATE hooks called
// on the SAME jobID in the SAME render (jobprogress.js's JobProgress), so
// their effects both fire before either's fetch has resolved - readCache
// alone is not enough, since it is only filled once a fetch's .then runs.
// Caching the promise itself closes that window: the second hook finds the
// first's request already in flight and awaits it instead of starting its
// own.
const pendingReads = new Map();

/**
 * useJobResultWarnings returns the finished job's own result warnings - the
 * sentences a result carries for the user to act on (issue 463: a flag-only
 * profile switch's recovery notice) - or an empty list.
 *
 * Called UNCONDITIONALLY, like every reader here - every caller's own
 * render must reach this before any early return, the same rule C1 (unit 6
 * fix wave) exists to enforce everywhere else in this application.
 */
export function useJobResultWarnings(jobID, jobState) {
  return useJobResultRead(jobID, jobState)?.warnings ?? NO_WARNINGS;
}

// useJobResultPurge returns the completed purge's additive file outcome,
// when it has one. It shares the same cached status request as every other
// result reader, so adding this detail never adds another poll.
export function useJobResultPurge(jobID, jobState) {
  return useJobResultRead(jobID, jobState)?.purge ?? null;
}

// useJobResultVerify returns a finished verify_fix job's repair outcome -
// {repaired, attention}, each a list of findings - or null when the job is
// not one, is still running, or repaired nothing and left nothing failing.
// Same shared read as every other result reader (issue 517).
export function useJobResultVerify(jobID, jobState) {
  return useJobResultRead(jobID, jobState)?.verify ?? null;
}

// useJobResultFailures returns a finished update batch's failed items
// (failures.js#batchFailuresFor) - each one's name, attempted change and
// reason, and its page and whether the file has to be fetched by hand when
// the failure was a download - or an empty list. Same shared read (issues
// 530 and 533).
export function useJobResultFailures(jobID, jobState) {
  return useJobResultRead(jobID, jobState)?.failures ?? NO_FAILURES;
}

// useJobResultPlugins returns what a finished job did to the game's plugin
// list (issue 616: core.PluginListResult, a deploy's or a purge's
// `plugins`), or null. Same shared read.
export function useJobResultPlugins(jobID, jobState) {
  return useJobResultRead(jobID, jobState)?.plugins ?? null;
}

// useJobResultLoader returns the loader refusal a finished job's result
// carries when the job refused before writing anything (issue 602), or
// null. Same shared read.
export function useJobResultLoader(jobID, jobState) {
  return useJobResultRead(jobID, jobState)?.loader ?? null;
}

const NO_WARNINGS = [];

/**
 * RESULT_WARNING_KINDS are the job kinds whose readout shows the result's
 * warnings where the job finished, and waits for the user to read them: a
 * switch's recovery notice (issue 463), and every removal that can hand a
 * shared path to the next mod in load order (issue 633) - with the other
 * sentences those results carry for the user (a file left as theirs, a
 * merged-artifact sync that failed), which the CLI prints too.
 */
export const RESULT_WARNING_KINDS = new Set([
  "switch",
  "uninstall",
  "disable",
  "updates",
  "rollback",
  "update_from_archive",
  "mod_copy",
  "profile_apply",
  // An install, an installer reconfigure and a profile import replace a
  // live version of a mod, which hands paths on too.
  "install",
  "mod_fomod",
  "profile_import",
]);
// PLUGIN_LIST_KINDS are the job kinds whose result has a `plugins` field.
const PLUGIN_LIST_KINDS = new Set(["deploy", "purge"]);
const NO_FAILURES = [];

/** readResult is what the cache holds for one finished job's status/result. */
function readResult(status) {
  const result = status?.result;
  const warnings = resultWarnings(status?.kind, result);
  const kept = Array.isArray(result?.kept) ? result.kept : [];
  const removedPaths = Number.isSafeInteger(result?.removed_paths)
    ? result.removed_paths
    : 0;
  // Several non-purge results also happen to have a `kept` field. The job
  // kind is the closed discriminator here, so an uninstall or a mod toggle
  // never receives purge's completion copy by coincidence.
  const purge =
    status?.kind === "purge" && (kept.length > 0 || removedPaths > 0)
      ? result
      : null;
  // core.VerifyReport: the findings are one level down, under `result`.
  const outcome =
    status?.kind === "verify_fix"
      ? repairOutcome(result?.result?.findings)
      : null;
  const verify =
    outcome && (outcome.repaired.length > 0 || outcome.attention.length > 0)
      ? outcome
      : null;
  const failures =
    status?.kind === "updates" ? batchFailuresFor(result) : NO_FAILURES;
  // Issue 602: an install batch that refused before writing anything still
  // succeeds as a job (its document lists every mod), so the loader
  // refusal behind it is read from the result rather than an error.
  const loader = result?.refused ? (result.loader_required ?? null) : null;
  // Issue 616: the plugin list, on the kinds whose result carries one.
  const plugins =
    PLUGIN_LIST_KINDS.has(status?.kind) &&
    result?.plugins &&
    typeof result.plugins.status === "string"
      ? result.plugins
      : null;
  return { warnings, purge, verify, failures, loader, plugins };
}

/** resultWarnings is a result's warnings: its own, and - an update batch's
 * (issue 633) - each applied item's. */
function resultWarnings(kind, result) {
  const own = Array.isArray(result?.warnings) ? result.warnings : [];
  const items =
    kind === "updates" && Array.isArray(result?.applied)
      ? result.applied.flatMap((a) =>
          Array.isArray(a?.warnings) ? a.warnings : [],
        )
      : [];
  const all = [...own, ...items].filter(
    (w) => typeof w === "string" && w !== "",
  );
  return all.length > 0 ? all : NO_WARNINGS;
}

/**
 * fetchJobResult is jobID's finished-job read (readResult's shape, or null
 * for a job the registry no longer holds), through the same cache and
 * in-flight map every hook above shares - so a caller outside a component
 * (main.js deciding whether a readout may be released) costs no second
 * request when a readout is also showing the job.
 */
export function fetchJobResult(jobID) {
  if (readCache.has(jobID)) return Promise.resolve(readCache.get(jobID));
  let read = pendingReads.get(jobID);
  if (!read) {
    read = jobStatus(jobID).then(
      (status) => readResult(status),
      () => {
        // A job the registry has already evicted (jobs.go's retention
        // limit) - honestly null, same as jobhistory.js's own "a gap in
        // history is honest, a blank page over one missing job is not".
        return null;
      },
    );
    pendingReads.set(jobID, read);
    read.then((computed) => {
      readCache.set(jobID, computed);
      pendingReads.delete(jobID);
    });
  }
  return read;
}

/**
 * readoutHasNextStep says whether a SUCCEEDED job's readout is carrying
 * something the user may still need to act on or read: a batch whose items
 * failed (the job's own state is "succeeded" all the same - the summary's
 * own tally says so), each failed update's entry and its download way out ("Open
 * on <source>", "Update from file…"), a purge's or a repair's own outcome, a
 * profile switch's recovery notice. Such a readout waits for the user, as a
 * failed job's does (main.js#releaseSucceededOrigin); only a bare "Done" is
 * handed back on a timer. `read` is fetchJobResult's answer; null (no
 * result to look at) holds nothing, as before.
 */
export function readoutHasNextStep(summary, read) {
  const tally = summary?.tally;
  if (tally && resultTallyTone(tally) !== "succeeded") return true;
  if (!read) return false;
  if (read.failures.length > 0 || read.purge || read.verify) return true;
  // Issue 616: a plugin list with findings, or one a purge put back, is
  // something to read before the readout goes.
  if (
    read.plugins &&
    ((read.plugins.findings?.length ?? 0) > 0 ||
      read.plugins.status === "restored" ||
      read.plugins.status === "removed")
  )
    return true;
  return RESULT_WARNING_KINDS.has(summary?.kind) && read.warnings.length > 0;
}

/**
 * resultMayHoldNextStep says whether readoutHasNextStep could need the job's
 * RESULT to answer for summary - a purge's or a repair's own outcome, a
 * switch's recovery notice, an update batch's failed items - rather than
 * the summary alone. A batch's failures are on its tally already, so this
 * only ever saves a read; it never changes the answer (issue 583: the
 * toast for every other kind no longer waits on a second round trip).
 */
export function resultMayHoldNextStep(summary) {
  // A tally answers for its batch's failures, which are counted on it -
  // but not for its warnings (issue 633: an update batch whose items handed
  // shared paths on), so a batch of a kind that shows them is read.
  if (summary?.tally && !RESULT_WARNING_KINDS.has(summary?.kind)) return false;
  return RESULT_NEXT_STEP_KINDS.has(summary?.kind);
}

// RESULT_NEXT_STEP_KINDS is every kind readResult reads a next step from.
const RESULT_NEXT_STEP_KINDS = new Set([
  "deploy",
  "purge",
  "verify_fix",
  ...RESULT_WARNING_KINDS,
]);

function useJobResultRead(jobID, jobState) {
  const [read, setRead] = useState(() => readCache.get(jobID) ?? null);

  useEffect(() => {
    if (!jobID || jobState === "running") return;
    let cancelled = false;
    fetchJobResult(jobID).then((computed) => {
      if (!cancelled) setRead(computed);
    });
    return () => {
      cancelled = true;
    };
  }, [jobID, jobState]);

  return read;
}
