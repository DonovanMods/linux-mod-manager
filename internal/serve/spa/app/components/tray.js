// tray.js - the activity bell and the tray it opens
// (docs/plans/2026-08-31-serve-spa-design.md §Jobs: "a top-bar activity
// bell collects running/queued/recent/failed jobs").
//
// The tray is the design's "quick path inline, full path one click away"
// applied to progress: every job the registry still retains, at a glance,
// with each entry expanding to that job's own phase-by-phase event stream.
// Two streams back it, and they are different on purpose - the multiplexed
// session stream (activity.js) keeps the LIST live while the tray is shut,
// and a per-job stream is opened only for the entry someone opened.
//
// Sections are ordered by what needs a person: running, then queued, then
// failures (which carry a next step), then the merely-finished. The design
// enumerates the same four contents; putting failures above successes is
// this file's own call, on the grounds that a tray is opened to find out
// what went wrong far more often than to admire what went right.

import { html, useEffect, useRef, useState } from "../render.js";
import { followJob } from "../sse.js";
import {
  frameFromEvent,
  jobStateLabel,
  progressText,
  progressFraction,
} from "../progress.js";
import { DocumentView } from "./documentview.js";
import { nextStepFor } from "../failures.js";

// maxStreamedEvents caps what one expanded entry keeps. core's downloader
// emits a DownloadEvent per read - thousands for a large mod - and the
// per-job stream deliberately does NOT coalesce them the way the activity
// stream does (activity.go's gate), because a viewer who opened a job asked
// for everything. Everything, in a browser, still has to be bounded: the
// newest are kept, which is the end a person reads first.
const maxStreamedEvents = 200;

/**
 * ActivityBell is the top bar's job surface: a badge that counts, and the
 * tray beneath it.
 *
 * The badge counts what is HAPPENING (running and queued jobs). When
 * nothing is happening it falls back to counting failures the user has not
 * opened the tray since - because a finished-but-failed deploy that leaves
 * a silent bell is exactly the outcome a bell exists to prevent.
 */
export function ActivityBell({ state, deepLinkJob, open, onOpen, onClose }) {
  const jobs = state.jobsIndex ?? [];
  const [acknowledgedAt, setAcknowledgedAt] = useState(0);

  const active = jobs.filter((job) => job.state === "running");
  const unseenFailures = jobs.filter(
    (job) =>
      job.state === "failed" && Date.parse(job.ended_at || 0) > acknowledgedAt,
  );
  const count = active.length || unseenFailures.length;
  const tone = active.length > 0 ? "active" : "failed";

  function toggle() {
    if (open) {
      onClose();
      return;
    }
    setAcknowledgedAt(Date.now());
    onOpen();
  }

  return html`
    <div class="picker activity-bell">
      <button
        type="button"
        class="picker__trigger activity-bell__trigger"
        aria-label=${`Activity (${count})`}
        onClick=${toggle}
      >
        🔔${
          count > 0 &&
          html`<span class="activity-bell__count activity-bell__count--${tone}"
            >${count}</span
          >`
        }
      </button>
      ${
        open &&
        html`<${ActivityTray}
          state=${state}
          jobs=${jobs}
          deepLinkJob=${deepLinkJob}
        />`
      }
    </div>
  `;
}

/** ActivityTray renders the retained jobs, grouped by what they need. */
function ActivityTray({ state, jobs, deepLinkJob }) {
  // The expanded entry starts at whatever ?job= named (the deleted
  // /jobs/{id} page's 301 target, spa.go), and follows it if the URL
  // changes underneath an open tray.
  const [expanded, setExpanded] = useState(deepLinkJob || "");
  useEffect(() => {
    if (deepLinkJob) setExpanded(deepLinkJob);
  }, [deepLinkJob]);

  // A job's label needs its latest frame as well as its summary: a summary
  // is only refreshed at the job's start and end, so the frame is what says
  // a running job has actually started working (progress.js).
  const labelOf = (job) => jobStateLabel(job, state.jobProgress?.[job.id]);
  const running = jobs.filter((j) => labelOf(j) === "running");
  const queued = jobs.filter((j) => labelOf(j) === "queued");
  const failed = jobs.filter((j) => j.state === "failed");
  const done = jobs.filter((j) => j.state === "succeeded");

  const missingDeepLink =
    deepLinkJob && !jobs.some((job) => job.id === deepLinkJob);

  const section = (title, rows) =>
    rows.length > 0 &&
    html`
      <li class="tray__section">
        <p class="tray__section-title">${title} (${rows.length})</p>
        <ul class="tray__rows">
          ${rows.map(
            (job) => html`
              <${TrayRow}
                key=${job.id}
                job=${job}
                frame=${state.jobProgress?.[job.id]}
                expanded=${expanded === job.id}
                onToggle=${() => setExpanded(expanded === job.id ? "" : job.id)}
              />
            `,
          )}
        </ul>
      </li>
    `;

  return html`
    <ul class="picker__menu tray">
      ${
        state.activityError &&
        html`<li class="tray__error">
          ${state.activityError} — reload to reconnect.
        </li>`
      }
      ${
        missingDeepLink &&
        html`<li class="tray__error">
          That job is no longer retained; the tray keeps the most recent ones
          only.
        </li>`
      }
      ${
        jobs.length === 0 && !state.activityError
          ? html`<li class="tray__empty">No activity yet.</li>`
          : html`
              ${section("Running", running)} ${section("Queued", queued)}
              ${section("Failed", failed)} ${section("Recent", done)}
            `
      }
    </ul>
  `;
}

/** TrayRow is one job: what it is, how it is going, and - once expanded -
 * everything it has said. */
function TrayRow({ job, frame, expanded, onToggle }) {
  const label = jobStateLabel(job, frame);
  const fraction = progressFraction(frame);

  return html`
    <li class="tray__row" data-job=${job.id} data-state=${label}>
      <button
        type="button"
        class="tray__summary"
        aria-expanded=${expanded ? "true" : "false"}
        onClick=${onToggle}
      >
        <span class="tray__kind">${job.kind}</span>
        <span class="tray__state tray__state--${job.state}">${label}</span>
        <span class="tray__caret">${expanded ? "▾" : "▸"}</span>
      </button>

      ${
        job.state === "running" &&
        html`
          <div class="tray__progress">
            <div
              class="job-progress__bar ${fraction === null ? "job-progress__bar--indeterminate" : ""}"
            >
              <div
                class="job-progress__fill"
                style=${fraction === null ? "" : `width: ${Math.round(fraction * 100)}%`}
              ></div>
            </div>
            <span class="tray__detail"
              >${progressText(frame) || "Working…"}</span
            >
          </div>
        `
      }
      ${job.state === "failed" && html`<${FailureNextStep} job=${job} />`}
      ${expanded && html`<${JobEventStream} jobID=${job.id} />`}
    </li>
  `;
}

/**
 * FailureNextStep renders a failed job's envelope and the next step it
 * implies (design doc §Jobs: "failures with their next step inline - a
 * conflict shows Overwrite? right in the tray").
 *
 * WHICH step is failures.js's decision, taken from the envelope's typed
 * details; this component only renders it. The details themselves are shown
 * in full whether or not they imply an action, because a failure whose
 * reason is hidden is the one thing worse than a failure with no next step.
 */
function FailureNextStep({ job }) {
  const envelope = job.error ?? {};
  const step = nextStepFor(envelope);

  return html`
    <div class="tray__failure">
      <p class="tray__failure-message">${envelope.error ?? "failed"}</p>
      ${
        step &&
        html`
          <button
            type="button"
            class="button button--small"
            data-action=${step.action}
            disabled=${Boolean(step.pending)}
            title=${step.pending ?? undefined}
          >
            ${step.label}
          </button>
        `
      }
      ${envelope.details && html`<${DocumentView} value=${envelope.details} />`}
    </div>
  `;
}

/**
 * JobEventStream is the "full path one click away": one job's own events,
 * from GET /api/v1/jobs/{id}/events.
 *
 * Opened only while the entry is expanded, and closed on collapse - the
 * effect's cleanup is what guarantees that, and without it a tray opened
 * and closed a few times would leave a stream per expansion attached to the
 * server for the life of the tab. A finished job replays its retained ring
 * and terminates immediately, which is why this works for history as well
 * as for live progress.
 */
function JobEventStream({ jobID }) {
  const [events, setEvents] = useState([]);
  const [ended, setEnded] = useState(false);
  // seq gives every arriving event a stable key: two identical download
  // ticks are genuinely equal as data, so nothing in the payload can key
  // them apart.
  const seq = useRef(0);

  useEffect(() => {
    setEvents([]);
    setEnded(false);
    return followJob(jobID, {
      onEvent: (event) =>
        setEvents((previous) =>
          [
            ...previous,
            { key: ++seq.current, frame: frameFromEvent(event) },
          ].slice(-maxStreamedEvents),
        ),
      onDone: () => setEnded(true),
    });
  }, [jobID]);

  return html`
    <ol class="tray__events" data-job=${jobID}>
      ${events.map(
        (entry) => html`
          <li key=${entry.key} class="tray__event">
            ${progressText(entry.frame)}
          </li>
        `,
      )}
      ${
        events.length === 0 &&
        html`<li class="tray__event tray__event--empty">
          ${ended ? "This job reported no events." : "Waiting for events…"}
        </li>`
      }
    </ol>
  `;
}
