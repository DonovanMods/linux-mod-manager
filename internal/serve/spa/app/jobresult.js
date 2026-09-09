// jobresult.js - fetching a finished job's own RESULT document for an
// honest outcome tally (I3, unit 6 fix wave).
//
// jobSummary (activity.go's own doc comment: "jobStatus without Result...
// fifty result documents in one poll would make the cheapest request in
// the application the most expensive one") never carries a job's Result -
// so a control that wants progress.js#resultTally's "n applied / m failed"
// instead of a bare "Done" has to make its OWN read of the full document
// (api.js#jobStatus), same as jobhistory.js already does for a different
// reason. Kept out of progress.js on purpose: that module stays pure and
// fetch-free, rendering only what it is handed.
import { useEffect, useState } from "./render.js";
import { jobStatus } from "./api.js";
import { resultTally } from "./progress.js";

// tallyCache is a page-lifetime Map from job id to its own (possibly null)
// tally, so the SAME finished job showing in two places at once (a row's
// own InlineJob AND the tray's entry for it) fetches its result exactly
// once, and neither ever re-fetches on a later re-render.
const tallyCache = new Map();

/**
 * useJobResultTally returns jobID's own resultTally, or null while the job
 * is still running, its result hasn't been fetched yet, or its result
 * carries no tally-shaped outcome at all.
 *
 * Called UNCONDITIONALLY - every caller's own render must reach this before
 * any early return, the same rule C1 (unit 6 fix wave) exists to enforce
 * everywhere else in this application.
 */
export function useJobResultTally(jobID, jobState) {
  const [tally, setTally] = useState(() => tallyCache.get(jobID) ?? null);

  useEffect(() => {
    if (!jobID || jobState === "running") return;
    if (tallyCache.has(jobID)) {
      setTally(tallyCache.get(jobID));
      return;
    }
    let cancelled = false;
    jobStatus(jobID).then(
      (status) => {
        const computed = resultTally(status.result);
        tallyCache.set(jobID, computed);
        if (!cancelled) setTally(computed);
      },
      () => {
        // A job the registry has already evicted (jobs.go's retention
        // limit) - honestly null, same as jobhistory.js's own "a gap in
        // history is honest, a blank page over one missing job is not".
        if (!cancelled) setTally(null);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [jobID, jobState]);

  return tally;
}
