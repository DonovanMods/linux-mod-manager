// omnibarresults.js - the omnibar's fan-out, appended below the library in
// place (issue 331, design doc §Search: "Enter fans out to the game's sources and
// appends 'From sources (n)' rows in place below the library").
//
// Rendered only while state.omnibarSearch still answers the LIVE omnibar
// text: typing past a fan-out (without pressing Enter again) hides this
// section rather than showing an increasingly stale answer to a question
// nobody is asking anymore - no explicit "clear" action needed, the render
// gate does it for free.
//
// The fan-out is capped and never pages (main.js#OMNIBAR_FANOUT_LIMIT), so a
// ready report ends with "See all results" - the way to the dedicated search
// page (searchpage.js) for the same query, which does page (issue 500).
//
// A pasted collection link gets the search page's own offer to import it
// (collectionoffer.js, issue 650): this is the search box a user pastes
// into, and a collection's address finds no mods.
//
// Issue 651: for a game whose collections can be searched, "Search
// collections" beside "See all results" opens the search page's Collections
// tab for the same query. Collection hits are not fanned out here: the fan-out
// is a few mod rows under the library, and a second search of a different
// kind on every Enter would cost a NexusMods request each time for rows a
// user who wanted collections reads better on the page built for them.

import { html } from "../render.js";
import { contextPath, navigate } from "../router.js";
import {
  SourceResultsList,
  skippedSignInNotice,
  warningsCaption,
} from "./searchresults.js";
import { Arrow } from "./arrow.js";
import { CollectionOffer } from "./collectionoffer.js";
import { collectionLink } from "../collectionlink.js";
import {
  COLLECTIONS,
  collectionsSearchable,
  searchPath,
} from "../searchtype.js";

export function OmnibarResults({ omnibarSearch, query, state, actions }) {
  const q = (query ?? "").trim();
  if (!omnibarSearch || omnibarSearch.query !== q) return null;

  if (omnibarSearch.status === "loading") {
    return html`
      <section class="library omnibar-results">
        <p class="app-booting">Searching sources…</p>
        ${
          omnibarSearch.indexNotice &&
          html`<p class="index-notice" data-testid="index-notice">
            ${omnibarSearch.indexNotice}
          </p>`
        }
      </section>
    `;
  }

  const collection = collectionLink(q);

  if (omnibarSearch.status === "error") {
    return html`
      <section class="library omnibar-results">
        <h2 class="section-header">From sources</h2>
        <${CollectionOffer}
          link=${collection}
          state=${state}
          actions=${actions}
          noResults=${true}
        />
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
  const route = state.route;
  const home = contextPath(route.game, route.profile);
  const allResultsPath = searchPath(home, q);
  const collectionsPath = collectionsSearchable(state.status)
    ? searchPath(home, q, COLLECTIONS)
    : "";

  return html`
    <section class="library omnibar-results">
      <h2 class="section-header">
        From sources (${hits.length})${warningsCaption(warnings)}
      </h2>
      <${CollectionOffer}
        link=${collection}
        state=${state}
        actions=${actions}
        noResults=${hits.length === 0}
      />
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
      <p class="omnibar-results__all">
        <a
          class="button button--small"
          data-testid="see-all-results"
          href=${allResultsPath}
          onClick=${(e) => {
            e.preventDefault();
            navigate(allResultsPath);
          }}
          >See all results${report.has_more ? " (more available)" : ""}${" "}
          <${Arrow} dir="right"
        /></a>
        ${
          collectionsPath &&
          html`${" "}<a
              class="button button--small"
              data-testid="search-collections"
              href=${collectionsPath}
              onClick=${(e) => {
                e.preventDefault();
                navigate(collectionsPath);
              }}
              >Search collections <${Arrow} dir="right"
            /></a>`
        }
      </p>
    </section>
  `;
}
