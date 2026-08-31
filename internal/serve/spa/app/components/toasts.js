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

/**
 * Toasts renders the store's toast list, newest at the bottom.
 *
 * A toast that names a job offers the one thing a user wants next from it -
 * the job's own event stream, which is the activity tray opened on that
 * entry (?job=, the same annotation the deleted /jobs/{id} page's 301 now
 * points at). It is offered only on the home route, which is the only route
 * that has a tray to open.
 */
export function Toasts({ toasts, route, onDismiss }) {
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
              ${toast.detail && html`<p class="toast__detail">${toast.detail}</p>`}
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
