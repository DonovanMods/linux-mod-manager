// toasts.js - the completions you would otherwise have missed.
//
// The design's rule is narrow and worth keeping narrow: "job completion/
// failure when its origin isn't on-screen; never for things in view"
// (docs/plans/2026-08-31-serve-spa-design.md §Jobs). A toast for something
// the user is already watching is noise, and noise is what makes people
// stop reading toasts - including the one that mattered.
//
// Who decides is main.js's onJobDone, using activity.js's origin registry;
// this component only renders what that decision produced. It is mounted at
// the application root, for every route, because the point of a toast is
// that it finds you where you went.
//
// Issue 655 added the running toast: a job whose control is not on screen
// (a collection import confirmed in a modal that then closed) shows its
// progress here while it runs - main.js#showDetachedProgress - and that
// toast gives way to the job's outcome when it ends.

import { html } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { codeSpans } from "../errortext.js";
import { isOriginMounted } from "../activity.js";
import {
  downloadText,
  humanizePhase,
  jobRunningLabel,
  progressFraction,
  updateStepFraction,
} from "../progress.js";
import { ItemFailureList } from "./errordetails.js";

// maxRunningToasts is how many running jobs get a progress toast each;
// more share one (RunningGroupBody).
const maxRunningToasts = 3;

// toastItemLimit is how many of a failure's per-mod lines a toast shows; the
// tray, one click away, shows them all.
const toastItemLimit = 5;

/**
 * Toasts renders the store's toast list, newest at the bottom.
 *
 * A toast that names a job offers the one thing a user wants next from it -
 * the job's own event stream and its whole error, which is the activity
 * tray opened on that entry (?job=, the same annotation the deleted
 * /jobs/{id} page's 301 now points at). The tray lives on the home route,
 * so from any other route "Show in activity" goes there.
 *
 * A toast may ALSO name one action from the actions object, by key, with
 * its own label and an optional argument (issue 334, the N7 carry; issue
 * 655's "Back to import"): a profile import is started from inside a modal,
 * and the confirm-plan modal REPLACES that modal in the shared slot
 * ("modals stack at most one deep"), so by the time the import finishes the
 * surface that would have shown its result is gone. The name is a string
 * rather than a function because it travels through the store, which holds
 * plain data - and it is looked up defensively, so a toast naming an action
 * this build no longer has renders without it rather than throwing over a
 * completion notice.
 *
 * The actions sit in one .form-actions row (docs/control-system.md), so two
 * of them are two spaced controls rather than one run of text.
 */
export function Toasts({
  toasts,
  route,
  onDismiss,
  actions,
  progress,
  positions,
}) {
  // A running toast whose control is back on screen hides: the control is
  // showing that job's progress itself.
  const visible = (toasts ?? []).filter(
    (toast) => toast.tone !== "running" || !isOriginMounted(toast.origin),
  );
  // Past maxRunningToasts, the running jobs share one toast that counts
  // them (issue 655 review): a stack of progress cards would cover the page.
  const running = visible.filter((toast) => toast.tone === "running");
  const list =
    running.length > maxRunningToasts
      ? [
          ...visible.filter((toast) => toast.tone !== "running"),
          {
            id: "running-group",
            tone: "running",
            group: running,
            jobID: running[0].jobID,
          },
        ]
      : visible;
  if (list.length === 0) return null;

  // "Show in activity" opens the tray on Mission Control. From Setup it is
  // not offered: the YAML and game editors' drafts there are the page's own
  // state, and leaving would discard them (issue 655 review S5).
  const canOpenTray = Boolean(route?.game) && route?.view !== "setup";
  // The chooser has no game to import into.
  const canAct = route?.view !== "chooser";

  return html`
    <div class="toasts" role="region" aria-label="Recent activity">
      ${list.map((toast) => {
        const hasAction =
          canAct &&
          toast.action &&
          typeof actions?.[toast.action] === "function";
        const showActivity = canOpenTray && toast.jobID;
        // A running toast's live region is its title alone (RunningToastBody):
        // its count changes with every frame, and a status role on the whole
        // card would announce each one.
        const role =
          toast.tone === "failure"
            ? "alert"
            : toast.tone === "running"
              ? null
              : "status";
        return html`
          <div
            key=${toast.id}
            class="toast toast--${toast.tone}"
            role=${role}
            data-job=${toast.group ? null : toast.jobID || null}
          >
            <div class="toast__body">
              ${
                toast.group
                  ? html`<${RunningGroupBody} toasts=${toast.group} />`
                  : toast.tone === "running"
                    ? html`<${RunningToastBody}
                        toast=${toast}
                        frame=${progress?.[toast.jobID]}
                        position=${positions?.[toast.jobID]}
                      />`
                    : html`
                        <p class="toast__title">${toast.title}</p>
                        ${
                          toast.detail &&
                          html`<p class="toast__detail">
                            ${codeSpans(toast.detail)}
                          </p>`
                        }
                        <${ItemFailureList}
                          items=${toast.items}
                          limit=${toastItemLimit}
                          summary=${true}
                        />
                      `
              }
              ${
                (hasAction || showActivity) &&
                html`<div class="form-actions toast__actions">
                  ${
                    hasAction &&
                    html`
                      <button
                        type="button"
                        class="button button--link toast__link"
                        data-action="toast-action"
                        onClick=${() => {
                          onDismiss(toast.id);
                          actions[toast.action](toast.actionArg);
                        }}
                      >
                        ${toast.actionLabel}
                      </button>
                    `
                  }
                  ${
                    showActivity &&
                    html`
                      <button
                        type="button"
                        class="button button--link toast__link"
                        data-action="toast-activity"
                        onClick=${() => {
                          onDismiss(toast.id);
                          navigate(
                            `${contextPath(route.game, route.profile)}?job=${encodeURIComponent(toast.jobID)}`,
                          );
                        }}
                      >
                        Show in activity
                      </button>
                    `
                  }
                </div>`
              }
            </div>
            <button
              type="button"
              class="button button--quiet button--icon button--small toast__dismiss"
              aria-label="Dismiss"
              onClick=${() =>
                toast.group
                  ? toast.group.forEach((each) => onDismiss(each.id))
                  : onDismiss(toast.id)}
            >
              ✕
            </button>
          </div>
        `;
      })}
    </div>
  `;
}

/**
 * RunningToastBody is a running job's progress (issue 655): what it is
 * ("Importing collection…"), how far through its items it is ("12 of 40" -
 * the newest frame that counted, activity.js's jobPositions), the item it is
 * on, and the current download's own progress when one is under way.
 */
function RunningToastBody({ toast, frame, position }) {
  const label = runningLabel(toast);
  const counted = position?.total > 0;
  // Issue 664: an update's step is placed within its item's share.
  const stepped = updateStepFraction(frame, position);
  const fraction =
    stepped ??
    (counted
      ? Math.min(1, Math.max(0, (position.index ?? 0) / position.total))
      : progressFraction(frame));
  const parts = [];
  if (counted) {
    parts.push(`${position.index ?? 0} of ${position.total}`);
    if (position.mod_name) parts.push(position.mod_name);
    else if (position.phase) parts.push(humanizePhase(position.phase));
    // Issue 664: and the step the item's update is on ("Installing").
    if (stepped !== null && frame?.total === undefined && frame?.phase) {
      parts.push(humanizePhase(frame.phase));
    }
  } else if (frame?.phase) {
    parts.push(humanizePhase(frame.phase));
  }
  const bytes = downloadText(frame);
  if (bytes) parts.push(`downloading ${bytes}`);

  return html`
    <p class="toast__title" role="status">${label}…</p>
    <div
      class="job-progress__bar ${fraction === null ? "job-progress__bar--indeterminate" : ""}"
    >
      <div
        class="job-progress__fill"
        style=${fraction === null ? "" : `width: ${Math.round(fraction * 100)}%`}
      ></div>
    </div>
    <p class="toast__detail" data-testid="toast-progress">
      ${parts.join(" · ") || "Starting…"}
    </p>
  `;
}

/** runningLabel names a running toast's job ("Importing collection"). */
function runningLabel(toast) {
  return jobRunningLabel(toast.kind, {
    collection: toast.collection,
    origin: toast.auto ? "auto" : "",
  });
}

/** RunningGroupBody is the one toast several running jobs share: how many
 * there are, and what each is. */
function RunningGroupBody({ toasts }) {
  return html`
    <p class="toast__title" role="status">${toasts.length} jobs running…</p>
    <ul class="toast__jobs" data-testid="toast-running-group">
      ${toasts.map(
        (toast) => html`<li key=${toast.id}>${runningLabel(toast)}</li>`,
      )}
    </ul>
  `;
}
