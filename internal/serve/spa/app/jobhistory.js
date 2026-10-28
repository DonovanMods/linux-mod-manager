// jobhistory.js - deriving one mod's job history from the tray's own
// jobsIndex plus the full per-job document api.js#jobStatus exists to read.
//
// jobSummary (internal/serve/activity.go) carries no mod identity at all -
// the tray's own index is deliberately Result-less ("fifty result documents
// in one poll would make the cheapest request in the application the most
// expensive one", activity.go's own doc comment). The full mod page can
// afford what the tray cannot: one jobStatus fetch per CANDIDATE job,
// reading identity out of the one place it survives the trip - the job's
// own RESULT document. This is the design's "quick path inline, full path
// one click away" applied to a job list rather than to one job's progress,
// and it is what makes jobStatus a real consumer (issue 330 carry-5).
//
// Not every mutation kind's result carries a mod reference. enable/disable
// (core.EnableResult/DisableResult) and uninstall (core.UninstallResult)
// report only Changed/Warnings/Notes - no source/mod id at all - so a job
// that ran one of those cannot be attributed to a mod after the fact
// without a core wire change, and this unit makes exactly one of those
// (issue 330 carry-4, the deploy plan's Ref.Version - already spent).
// What CAN be attributed, because their result documents already carry it:
// "updates" (core.UpdateApplyResult.Mod / the batch failure list's own
// "source:id" string) and "rollback" (core.RollbackResult.Mod).
import { jobStatus } from "./api.js";

/** modHistoryKinds are the job kinds whose RESULT document says which
 * mod(s) it concerned. */
const modHistoryKinds = new Set(["updates", "rollback"]);

/** resultNamesMod reports whether a finished job's Result document concerns
 * sourceID/modID, checked structurally against each kind's own documented
 * shape (never by kind alone: "updates" is a BATCH, and most of its rows
 * concern some other mod entirely). */
function resultNamesMod(kind, result, sourceID, modID) {
  if (!result) return false;
  if (kind === "rollback") {
    return result.mod?.source_id === sourceID && result.mod?.mod_id === modID;
  }
  if (kind === "updates") {
    const key = `${sourceID}:${modID}`;
    // Applied and Skipped are core.UpdateApplyResults (a nested `mod` ref);
    // Failed carries the flat "source:id" key instead (issue 324's
    // UpdateBatchFailure). Skipped is the locked-ref outcome (#97) - a mod
    // whose batch DID concern it, so a history section that ignored it
    // would tell that mod's page nothing happened.
    const namesRef = (r) =>
      r.mod?.source_id === sourceID && r.mod?.mod_id === modID;
    return (
      (result.applied ?? []).some(namesRef) ||
      (result.skipped ?? []).some(namesRef) ||
      (result.failed ?? []).some((f) => f.mod === key)
    );
  }
  return false;
}

/**
 * candidateJobKey collapses jobsIndex to the one thing JobHistorySection's
 * effect (fullmodpage.js) actually depends on: WHICH finished
 * updates/rollback jobs exist, not jobsIndex's own array identity (issue
 * 330 carry-4, unit 6). jobsIndex gets a new reference on every
 * job_started/job_done frame in the WHOLE session (activity.js's
 * upsertSummary) - with the batch modals this unit adds, that includes
 * every enable/disable/uninstall/updates-batch job too, none of which this
 * section can ever attribute to a mod (modHistoryKinds). Keying the effect
 * on this string instead of the raw array means it only re-fires (and only
 * re-fetches) when a job THIS section could actually use appears or
 * finishes, not on every other kind's job the session happens to run.
 */
export function candidateJobKey(jobsIndex) {
  return (jobsIndex ?? [])
    .filter((j) => modHistoryKinds.has(j.kind) && j.state !== "running")
    .map((j) => `${j.id}:${j.state}`)
    .join(",");
}

/**
 * loadModJobHistory resolves jobsIndex to the FINISHED jobs that concerned
 * sourceID/modID, newest first (jobsIndex's own order) - one jobStatus
 * fetch per candidate (a non-running job whose kind is in
 * modHistoryKinds), run in parallel. A fetch that fails (the job was
 * evicted between the index read and this one, jobs.go's retention limit)
 * is dropped rather than thrown - a gap in history is honest, a blank page
 * over one missing job is not.
 */
export async function loadModJobHistory(jobsIndex, sourceID, modID) {
  const candidates = (jobsIndex ?? []).filter(
    (j) => modHistoryKinds.has(j.kind) && j.state !== "running",
  );
  const statuses = await Promise.all(
    candidates.map((j) => jobStatus(j.id).catch(() => null)),
  );
  return candidates
    .map((summary, i) => ({ summary, status: statuses[i] }))
    .filter(
      ({ status }) =>
        status && resultNamesMod(status.kind, status.result, sourceID, modID),
    )
    .map(({ status }) => status);
}
