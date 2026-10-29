// releasebanner.js - the page-wide "a newer lmm is available" notice (issue 568).
//
// Shown only when the server's cached release check says an update exists and
// the check is enabled. Dismissal is remembered PER VERSION in localStorage:
// dismissing 2.1.0 hides this notice until a newer release is announced.
// Every failure (the fetch, unreadable storage) degrades to showing nothing
// or to a dismissal that lasts only this page view.

import { html, useEffect, useState } from "../render.js";
import { getLmmRelease } from "../api.js";

/** RELEASE_BANNER_DISMISSED_KEY holds the dismissed `latest` version. */
export const RELEASE_BANNER_DISMISSED_KEY = "lmm.release-banner.dismissed";

function readDismissed() {
  try {
    return localStorage.getItem(RELEASE_BANNER_DISMISSED_KEY) ?? "";
  } catch {
    return "";
  }
}

function writeDismissed(version) {
  try {
    localStorage.setItem(RELEASE_BANNER_DISMISSED_KEY, version);
  } catch {
    // Nothing to persist to; the in-memory state still hides it this view.
  }
}

/** ReleaseBanner fetches the release status once on mount and renders the
 * dismissible notice, or nothing. */
export function ReleaseBanner() {
  const [status, setStatus] = useState(null);
  const [dismissed, setDismissed] = useState(readDismissed);

  useEffect(() => {
    let live = true;
    getLmmRelease()
      .then((s) => live && setStatus(s))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  if (!status || !status.update_available || !status.check_enabled) return null;
  if (!status.latest || dismissed === status.latest) return null;

  const install = status.install ?? {};
  return html`
    <section
      class="release-banner"
      aria-label="lmm update available"
      data-testid="release-banner"
    >
      <p class="release-banner__text">
        lmm ${status.latest} is available — you have ${status.current}.
        ${
          install.method === "package" &&
          install.upgrade_command &&
          html` Upgrade with <code>${install.upgrade_command}</code>.`
        }
        ${
          status.release_url &&
          html` <a
            href=${status.release_url}
            target="_blank"
            rel="noopener noreferrer"
            >Release notes</a
          >`
        }
      </p>
      <button
        type="button"
        class="button button--quiet button--icon release-banner__dismiss"
        aria-label=${`Dismiss the lmm ${status.latest} notice`}
        data-action="dismiss-release-banner"
        onClick=${() => {
          writeDismissed(status.latest);
          setDismissed(status.latest);
        }}
      >
        ✕
      </button>
    </section>
  `;
}
