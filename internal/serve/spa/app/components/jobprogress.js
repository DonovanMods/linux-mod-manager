// jobprogress.js - "the control you clicked morphs into its progress"
// (docs/plans/2026-08-31-serve-spa-design.md §Jobs).
//
// The morph is what makes a job feel like it belongs to the thing that
// started it rather than to a progress dialog somewhere else. It works
// through an ORIGIN - a stable string naming the control ("deploy", and
// later "install:fake/123", "mod:fake/123:uninstall") - which the store
// maps to the job id that control started. Any control can morph by
// wrapping itself in InlineJob; nothing else is required of it.
//
// A finished job stays on its control rather than vanishing: the design's
// "completion/failure resurface inline" is the same rule read from the
// other end - if you were watching it, you find out here, and only if you
// were NOT watching does it become a toast (activity.js's origin registry).

import { html, useEffect } from "../render.js";
import { registerOrigin } from "../activity.js";
import { progressText, progressFraction, jobStateLabel } from "../progress.js";

/**
 * InlineJob renders children when origin has no job, and that job's live
 * state when it does.
 *
 * It also registers origin as on-screen for as long as it is mounted, which
 * is what the toast rule reads: navigate away mid-deploy and the completion
 * finds you as a toast instead, because this effect's cleanup ran.
 */
export function InlineJob({ origin, state, actions, children }) {
  useEffect(() => registerOrigin(origin), [origin]);

  const jobID = state.origins?.[origin];
  if (!jobID) return children;

  const summary = (state.jobsIndex ?? []).find((row) => row.id === jobID);
  const frame = state.jobProgress?.[jobID];

  return html`<${JobProgress}
    jobID=${jobID}
    summary=${summary}
    frame=${frame}
    onDismiss=${() => actions.clearOrigin(origin)}
  />`;
}

/**
 * JobProgress is one job's inline readout: a bar while it runs, its outcome
 * once it ends.
 *
 * A job whose summary has not arrived yet (the job start returned, the
 * stream's job_started frame has not) renders as starting rather than as
 * nothing - the click must never look like it did nothing.
 */
export function JobProgress({ jobID, summary, frame, onDismiss }) {
  const state = summary?.state ?? "running";

  if (state === "running") {
    const fraction = progressFraction(frame);
    const label = progressText(frame) || startingLabel(summary);
    return html`
      <div
        class="job-progress"
        data-job=${jobID}
        data-state=${jobStateLabel(summary) || "running"}
        role="status"
      >
        <div
          class="job-progress__bar ${fraction === null ? "job-progress__bar--indeterminate" : ""}"
        >
          <div
            class="job-progress__fill"
            style=${fraction === null ? "" : `width: ${Math.round(fraction * 100)}%`}
          ></div>
        </div>
        <span class="job-progress__text">${label}</span>
      </div>
    `;
  }

  const failed = state === "failed";
  return html`
    <div
      class="job-progress job-progress--${state}"
      data-job=${jobID}
      data-state=${state}
      role="status"
    >
      <span class="job-progress__text">
        ${failed ? `Failed: ${summary?.error?.error ?? "unknown error"}` : "Done"}
      </span>
      <button
        type="button"
        class="job-progress__dismiss"
        aria-label="Dismiss"
        onClick=${onDismiss}
      >
        ✕
      </button>
    </div>
  `;
}

/** startingLabel is what a running job with nothing to report yet says.
 * "Queued" rather than "Working" when the job has emitted no event at all,
 * which is the closest an unqueued registry can honestly get (progress.js's
 * jobStateLabel). */
function startingLabel(summary) {
  return jobStateLabel(summary) === "queued" ? "Queued…" : "Working…";
}
