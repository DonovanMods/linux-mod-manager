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
import {
  progressText,
  progressFraction,
  jobStateLabel,
  resultTallyLabel,
  resultTallyTone,
} from "../progress.js";
import { useJobResultTally } from "../jobresult.js";
import { OverwriteButton } from "./tray.js";
import { explainerFor } from "../failures.js";

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
    actions=${actions}
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
 *
 * A failed job renders tray.js's own OverwriteButton beside its "Failed:
 * ..." text (I4, unit 5 fix wave: the action lives where the failure is
 * shown, not only in a tray a route may not even have) - actions is
 * optional so a caller with nothing to retry through (there is none today,
 * but nothing here should hard-require it) can still render every other
 * state.
 */
export function JobProgress({ jobID, summary, frame, actions, onDismiss }) {
  const state = summary?.state ?? "running";
  // Called unconditionally, before either branch below - C1 (unit 6 fix
  // wave)'s own rule applies here too: a hook called only on one side of a
  // conditional is exactly the pattern that let a stale value survive.
  const tally = useJobResultTally(jobID, state);

  if (state === "running") {
    const fraction = progressFraction(frame);
    const label = progressText(frame) || startingLabel(summary);
    return html`
      <div
        class="job-progress"
        data-job=${jobID}
        data-state=${jobStateLabel(summary, frame) || "running"}
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
  // issue 269 Tier 3: a Workshop download failure carries a reason the user can
  // act on (subscribe in Steam and use `lmm import --workshop`, install
  // steamcmd, ...). It renders here rather than only in the tray for the
  // same reason the Overwrite button does - the explanation belongs where
  // the failure is shown, which for an install is the mod panel or the
  // search result the user clicked. Dismissing it puts the Install action
  // straight back: nothing about this failure hides the button.
  const explainer = failed ? explainerFor(summary) : null;
  // I3, unit 6 fix wave: a batch job's own `state` is "succeeded" even when
  // every item inside it failed (progress.js#resultTally's own doc
  // comment) - the tone class follows the TALLY, not the bare state, for
  // exactly the cases that disagree with it.
  const tone = !failed && tally ? resultTallyTone(tally) : state;
  return html`
    <div
      class="job-progress job-progress--${tone} ${explainer ? "job-progress--explained" : ""}"
      data-job=${jobID}
      data-state=${state}
      role="status"
    >
      <span
        class="job-progress__text"
        title=${(tally?.skippedNotes ?? []).join("; ") || undefined}
      >
        ${
          failed
            ? `Failed: ${summary?.error?.error ?? "unknown error"}`
            : tally
              ? resultTallyLabel(tally)
              : "Done"
        }
      </span>
      ${
        failed &&
        summary &&
        actions &&
        html`<${OverwriteButton} job=${summary} actions=${actions} />`
      }
      <button
        type="button"
        class="job-progress__dismiss"
        aria-label="Dismiss"
        onClick=${onDismiss}
      >
        ✕
      </button>
      ${
        explainer &&
        html`<p
          class="job-progress__explainer"
          data-explainer=${explainer.tool || "reason"}
        >
          ${explainer.reason}
          ${
            explainer.outputTail &&
            html`<span class="job-progress__tool-output mono"
              >${explainer.outputTail}</span
            >`
          }
        </p>`
      }
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
