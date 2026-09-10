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

import { html } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { codeSpans } from "../errortext.js";

/**
 * Toasts renders the store's toast list, newest at the bottom.
 *
 * A toast that names a job offers the one thing a user wants next from it -
 * the job's own event stream, which is the activity tray opened on that
 * entry (?job=, the same annotation the deleted /jobs/{id} page's 301 now
 * points at). It is offered only on the home route, which is the only route
 * that has a tray to open.
 *
 * A toast may ALSO name one action from the actions object, by key, with
 * its own label (issue 334, the N7 carry): a profile import is started from
 * inside the profiles modal, and the confirm-plan modal REPLACES that modal
 * in the shared slot ("modals stack at most one deep"), so by the time the
 * import finishes the surface that would have shown its result is gone and
 * the user has to go and re-open it to see what they just created. The name
 * is a string rather than a function because it travels through the store,
 * which holds plain data - and it is looked up defensively, so a toast
 * naming an action this build no longer has renders without it rather than
 * throwing over a completion notice.
 */
export function Toasts({ toasts, route, onDismiss, actions }) {
  const list = toasts ?? [];
  if (list.length === 0) return null;

  const canOpenTray = route?.view === "home";

  return html`
    <div class="toasts" role="region" aria-label="Recent activity">
      ${list.map(
        (toast) => html`
          <div
            key=${toast.id}
            class="toast toast--${toast.tone}"
            role=${toast.tone === "failure" ? "alert" : "status"}
          >
            <div class="toast__body">
              <p class="toast__title">${toast.title}</p>
              ${toast.detail && html`<p class="toast__detail">${codeSpans(toast.detail)}</p>`}
              ${
                toast.action &&
                typeof actions?.[toast.action] === "function" &&
                html`
                  <button
                    type="button"
                    class="toast__link"
                    data-action="toast-action"
                    onClick=${() => {
                      onDismiss(toast.id);
                      actions[toast.action]();
                    }}
                  >
                    ${toast.actionLabel}
                  </button>
                `
              }
              ${
                canOpenTray &&
                toast.jobID &&
                html`
                  <button
                    type="button"
                    class="toast__link"
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
            </div>
            <button
              type="button"
              class="toast__dismiss"
              aria-label="Dismiss"
              onClick=${() => onDismiss(toast.id)}
            >
              ✕
            </button>
          </div>
        `,
      )}
    </div>
  `;
}
