// modpagelink.js - "Open on <source name>": a mod's page on its source.
//
// One component for every place a mod is shown (the slide-over, the full
// mod page, search and omnibar rows, the library row's menu) and for a failed
// download, so the safety rule and the markup exist exactly once.
//
// The link opens in a new tab with rel="noopener noreferrer": the source's
// page is somewhere else, and must not get a handle on this window or learn
// where the user came from. A plain link needs no change to the page's CSP.
//
// It renders NOTHING for a URL that is not a plain web address (weburl.js) -
// a missing or unsafe URL leaves no empty link, no "unavailable" text, and no
// href that could run script.

import { html } from "../render.js";
import { safeWebUrl } from "../weburl.js";
import { useSourceName } from "../sourcenames.js";

/**
 * ModPageLink renders the link, or nothing.
 *
 * `url` is the mod's source_url (or a typed download error's mod_url);
 * `sourceID` names the source for the link text; `modName` goes into the
 * accessible name so a screen reader's list of links says WHICH mod each one
 * is for. The accessible name begins with the visible text ("Open on Nexus
 * Mods"), as WCAG 2.5.3 (label in name) requires. `className` adds to the
 * base class, for a place that styles it as something else (a menu item).
 */
export function ModPageLink({ url, sourceID, modName, className }) {
  const href = safeWebUrl(url);
  const sourceName = useSourceName(sourceID, href !== "");
  if (!href) return null;
  const text = `Open on ${sourceName}`;
  const label = modName
    ? `${text}: ${modName} (opens in a new tab)`
    : `${text} (opens in a new tab)`;
  return html`<a
    class="mod-page-link ${className ?? ""}"
    href=${href}
    target="_blank"
    rel="noopener noreferrer"
    aria-label=${label}
    >${text}</a
  >`;
}
