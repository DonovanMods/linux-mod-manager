// releasebanner.js - the page-wide "a newer lmm is available" notice (issue 568).
//
// Shown only when the server's cached release check says an update exists and
// the check is enabled. Dismissal is remembered PER VERSION in localStorage:
// dismissing 2.1.0 hides this notice until a newer release is announced.
//
// Its second state (issue 569) comes first: a newer lmm is installed on disk
// than this server runs - `lmm self-update` replaced the binary under an
// `lmm serve` started by hand - so the notice asks for a restart instead.
// That one is local, so it shows whether or not the GitHub check is on, and
// its dismissal is remembered as "installed:<version>".
//
// Its third state (issue 586) is lmm having updated itself: the status's
// auto_update.last names an automatic update to the version this server
// runs, and the notice says so once - dismissed as "auto:<version>" - with
// how to turn automatic updates off after the first one. When the update
// replaced the binary under a hand-started server, the restart notice says
// it was automatic instead.
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

  if (!status) return null;
  const last = status.auto_update?.last;
  const optOut = last?.first
    ? html` To turn this off: <code>auto_update: false</code> in config.yaml, or Setup > Updates.`
    : "";
  if (status.installed) {
    const key = `installed:${status.installed}`;
    if (dismissed === key) return null;
    const automatic = last && last.to === status.installed;
    return html`
      <section
        class="release-banner"
        aria-label="lmm restart needed"
        data-testid="release-banner"
        data-state="installed"
      >
        <p class="release-banner__text">
          ${
            automatic
              ? html`lmm updated itself to v${status.installed}; restart${" "}
                  <code>lmm serve</code> to use it.${optOut}`
              : html`lmm v${status.installed} is installed; restart${" "}
                  <code>lmm serve</code> to use it.`
          }
        </p>
        <button
          type="button"
          class="button button--quiet button--icon release-banner__dismiss"
          aria-label=${`Dismiss the lmm ${status.installed} restart notice`}
          data-action="dismiss-release-banner"
          onClick=${() => {
            writeDismissed(key);
            setDismissed(key);
          }}
        >
          ✕
        </button>
      </section>
    `;
  }
  if (last && last.to === status.current && dismissed !== `auto:${last.to}`) {
    const key = `auto:${last.to}`;
    return html`
      <section
        class="release-banner"
        aria-label="lmm updated itself"
        data-testid="release-banner"
        data-state="auto-updated"
      >
        <p class="release-banner__text">lmm updated itself to v${last.to}.${optOut}</p>
        <button
          type="button"
          class="button button--quiet button--icon release-banner__dismiss"
          aria-label=${`Dismiss the lmm ${last.to} update notice`}
          data-action="dismiss-release-banner"
          onClick=${() => {
            writeDismissed(key);
            setDismissed(key);
          }}
        >
          ✕
        </button>
      </section>
    `;
  }
  if (!status.update_available || !status.check_enabled) return null;
  if (!status.latest || dismissed === status.latest) return null;

  const install = status.install ?? {};
  return html`
    <section
      class="release-banner"
      aria-label="lmm update available"
      data-testid="release-banner"
      data-state="available"
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
