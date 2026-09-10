// omnibarresults.js - the omnibar's fan-out, appended below the library in
// place (issue 331, design doc §Search: "Enter fans out to the game's sources and
// appends 'From sources (n)' rows in place below the library").
//
// Rendered only while state.omnibarSearch still answers the LIVE omnibar
// text: typing past a fan-out (without pressing Enter again) hides this
// section rather than showing an increasingly stale answer to a question
// nobody is asking anymore - no explicit "clear" action needed, the render
// gate does it for free.

import { html } from "../render.js";
import {
  SourceResultsList,
  skippedSignInNotice,
  warningsCaption,
} from "./searchresults.js";

export function OmnibarResults({ omnibarSearch, query, state, actions }) {
  const q = (query ?? "").trim();
  if (!omnibarSearch || omnibarSearch.query !== q) return null;

  if (omnibarSearch.status === "loading") {
    return html`
      <section class="library omnibar-results">
        <p class="app-booting">Searching sources…</p>
      </section>
    `;
  }

  if (omnibarSearch.status === "error") {
    return html`
      <section class="library omnibar-results">
        <h2 class="section-header">From sources</h2>
        <p class="empty-state__hint">
          Couldn't search sources: ${omnibarSearch.error}
        </p>
      </section>
    `;
  }

  const report = omnibarSearch.report;
  const hits = report.mods ?? [];
  const warnings = report.warnings ?? [];
  // Issue 383 (F1), the same rule the dedicated search page applies: a source
  // skipped for want of a key is named once, and never reported as a game
  // with nothing that can search.
  const skippedNotice = skippedSignInNotice(report.skipped_unauthenticated);

  return html`
    <section class="library omnibar-results">
      <h2 class="section-header">
        From sources (${hits.length})${warningsCaption(warnings)}
      </h2>
      ${
        hits.length === 0 &&
        skippedNotice !== "" &&
        html`<p class="empty-state__hint">${skippedNotice}</p>`
      }
      ${
        skippedNotice === "" &&
        report.attempted_count === 0 &&
        html`<p class="empty-state__hint">
          None of this game's sources support searching.
        </p>`
      }
      ${
        hits.length === 0 &&
        warnings.length === 0 &&
        report.attempted_count !== 0 &&
        html`<p class="empty-state__hint">No results from your sources.</p>`
      }
      <${SourceResultsList}
        hits=${hits}
        warnings=${warnings}
        state=${state}
        actions=${actions}
      />
    </section>
  `;
}
