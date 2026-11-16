// searchpage.js - the dedicated search page at /g/{game}/{profile}/search?q=
// (issue 331, design doc §Search: "the escape hatch for heavy browsing - source
// badges, star/download counts, summaries, category/source filters, sort,
// pagination"). Reached from a deep link, or the omnibar fan-out's "See all
// results" link (omnibarresults.js, issue 500) - the model for "pages that
// earn it" (design doc), so unlike Mission Control's inline fan-out this one
// actually leaves home: its own minimal header with a Back link, matching
// fullmodpage.js's own pattern for the same reason.
//
// issue 331's own pagination fetches a fresh page from the server
// (main.js#runSearchPage). Category/source filtering moved SERVER-SIDE
// there too (Important 1b, unit 5 fix wave): a filter change re-queries
// page 0 with that option, so the count this page renders answers the
// CATALOG for that filter, not a client-side slice of one page.
//
// Sort is server-side too (issue 503): it used to reorder the one page already
// fetched, which is not "most downloaded" of anything. Choosing one re-queries
// page 0 with ?sort=, so it orders the whole catalog the sources can order.
// The select offers only the sorts the report lists in sorts_available - a
// sort no source that answered can order by is never shown (searchsort.js).
//
// Issue 651: a game with a source whose collections can be searched
// (NexusMods) gets a Mods / Collections tab pair. The Collections tab is
// `lmm search --collections`: the same query, sort and pager over the game's
// collections, each with Import (collectionresults.js), and no category,
// source or tag filters, which mean nothing to a collection. Its query may be
// empty - the route ?type=collections with no ?q= lists the game's
// collections, as the CLI does with no query.

import { html } from "../render.js";
import { contextPath } from "../router.js";
import { RELEVANCE, effectiveSort, offeredSorts } from "../searchsort.js";
import { collectionLink } from "../collectionlink.js";
import { SourceResultsList, skippedSignInNotice } from "./searchresults.js";
import { AddAPIKeyButton } from "./addapikey.js";
import { ModPanel } from "./modpanel.js";
import { AwayBar } from "./awaybar.js";
import { codeSpans } from "../errortext.js";
import { ErrorDetails } from "./errordetails.js";
import { Arrow } from "./arrow.js";
import { CollectionOffer } from "./collectionoffer.js";
import { CollectionResultsList } from "./collectionresults.js";
import {
  COLLECTIONS,
  MODS,
  collectionsSearchable,
  knownSearchType,
} from "../searchtype.js";

// See tagsSupported below.
const TAG_CAPABLE_SOURCES = new Set(["nexusmods", "thunderstore"]);

export function SearchPage({ state, route, onThemeChange, actions }) {
  // No hooks: every filter, the sort included, lives in state.searchPage,
  // since a change to any of them re-fetches (main.js#runSearchPage).
  const searchPage = state.searchPage;
  const query = (route.q ?? "").trim();
  const canSearchCollections = collectionsSearchable(state?.status);
  // What this page searches: the route's ?type= (main.js runs the same
  // search). A game with no collection source gets no tabs, and a
  // collections link to it says why the search failed.
  const type = knownSearchType(route.type);
  const collections = type === COLLECTIONS;
  // A pasted collection link is not a search term, and searching for it
  // finds nothing. Offer what the user actually meant (issue 269 W2 for a
  // Workshop link, issue 650 for every other collection).
  const collection = collectionLink(query);
  const matches =
    searchPage &&
    searchPage.query === query &&
    (searchPage.type ?? MODS) === type;
  const report = matches ? searchPage.report : null;
  const hits = (collections ? report?.collections : report?.mods) ?? [];
  const facets = matches ? searchPage.facets : null;
  // Issue 383 (F1): the sources that CAN search but were left out for want of a
  // key. Reported apart from warnings and attempted_count, so the notice
  // below has to consult it rather than reading attempted_count 0 as "this
  // game has nothing that searches".
  const skippedNotice = skippedSignInNotice(report?.skipped_unauthenticated);

  // The sorts worth offering for THIS report, and the one to show selected:
  // the requested sort when the report offers it, relevance otherwise.
  const sorts = offeredSorts(report);
  const currentSort = effectiveSort(searchPage?.sort, report);

  // TAG_CAPABLE_SOURCES is the set of built-in sources whose search honours
  // ?tag= (core.SearchOptions.Tags). It is a client-side list because
  // nothing on the wire advertises the capability per source - the same
  // reason `lmm search --tag`'s own help says support varies. Kept narrow
  // deliberately: showing the filter where it does nothing is the failure
  // mode worth avoiding.
  //
  // thunderstore is in it because it DOES honour tags (issue 408 review nit
  // 13): SearchQuery.Tags and SearchQuery.Category both filter the same
  // Thunderstore categories, ANDed, which is exactly what `lmm search --tag`
  // does against it. Leaving it out made the README's "--category and --tag
  // both filter Thunderstore's own categories" true of the CLI and false of
  // the web UI.
  const tagsSupported = Object.keys(state?.status?.source_ids ?? {}).some(
    (id) => TAG_CAPABLE_SOURCES.has(id),
  );

  const home = contextPath(route.game, route.profile);
  const header = html`
    <${AwayBar}
      state=${state}
      route=${route}
      home=${home}
      onThemeChange=${onThemeChange}
      actions=${actions}
    />
  `;

  const tabs =
    (canSearchCollections || collections) &&
    html`<${SearchTypeTabs} type=${type} actions=${actions} />`;

  if (!query && !collections) {
    return html`
      ${header}
      <main id="main" class="app-main search-page">
        ${tabs}
        <${SearchPanel} tabbed=${Boolean(tabs)} type=${type}>
          <p class="empty-state__hint">
            Search from the omnibar (or a deep link with ?q=) to browse results
            here.
          </p>
        <//>
      </main>
    `;
  }

  if (!matches || searchPage.status === "loading") {
    return html`${header}
      <main id="main" class="app-main search-page">
        ${tabs}
        <${SearchPanel} tabbed=${Boolean(tabs)} type=${type}>
          <p class="app-booting">Searching…</p>
          ${
            matches &&
            searchPage.indexNotice &&
            html`<p class="index-notice" data-testid="index-notice">
              ${searchPage.indexNotice}
            </p>`
          }
        <//>
      </main>`;
  }

  if (searchPage.status === "error") {
    return html`
      ${header}
      <main id="main" class="app-main search-page">
        ${tabs}
        <${SearchPanel} tabbed=${Boolean(tabs)} type=${type}>
          <p class="app-error">
            Couldn't search: ${codeSpans(searchPage.error)}
          </p>
          <${ErrorDetails} details=${searchPage.details} />
          <${CollectionOffer}
            link=${collection}
            state=${state}
            actions=${actions}
            noResults=${true}
          />
        <//>
      </main>
    `;
  }

  // The header names what its own number IS (Important 1a): a page's own
  // hit count, never a bare "(n)" that reads as the catalog's total - there
  // is no catalog total on the wire (TotalResults is this page's own
  // pre-cap merge). "more available" is the has_more cue a reader needs to
  // know a Next page exists at all.
  const pageSummary = `Page ${searchPage.page + 1} · ${hits.length} on this page${report.has_more ? " · more available" : ""}`;
  // A collection search does have the catalog's total: the source counts
  // every match (core.CollectionSearchReport.total_results).
  const heading = collections
    ? `${searchPage.query ? `Collections matching “${searchPage.query}”` : "Collections"} — ${pageSummary} · ${report.total_results} in all`
    : `Results for “${searchPage.query}” — ${pageSummary}`;

  return html`
    ${header}
    <main
      id="main"
      class="app-main search-page"
      data-hydrated="true"
      data-search-type=${type}
    >
      ${tabs}
      <${SearchPanel} tabbed=${Boolean(tabs)} type=${type}>
        <${CollectionOffer}
          link=${collection}
          state=${state}
          actions=${actions}
          noResults=${
            // A bare slug is offered when the search, unfiltered, found
            // nothing - not when a filter narrowed a real word's hits away.
            hits.length === 0 &&
            searchPage.page === 0 &&
            !searchPage.category &&
            !searchPage.source &&
            !searchPage.tags
          }
        />
        <div class="search-page__toolbar">
          <h1 class="section-header">${heading}</h1>
          ${
            !collections &&
            (facets?.categories.length ?? 0) > 0 &&
            html`
              <label class="form-field form-field--inline library__control">
                Category
                <select
                  name="category"
                  value=${searchPage.category}
                  onChange=${(e) =>
                    actions.searchPageSetCategory(e.currentTarget.value)}
                >
                  <option value="">All</option>
                  ${facets.categories.map(
                    (c) => html`<option key=${c} value=${c}>${c}</option>`,
                  )}
                </select>
              </label>
            `
          }
          ${
            !collections &&
            (facets?.sourceIDs.length ?? 0) > 1 &&
            html`
              <label class="form-field form-field--inline library__control">
                Source
                <select
                  name="source"
                  value=${searchPage.source}
                  onChange=${(e) =>
                    actions.searchPageSetSource(e.currentTarget.value)}
                >
                  <option value="">All</option>
                  ${facets.sourceIDs.map(
                    (s) => html`<option key=${s} value=${s}>${s}</option>`,
                  )}
                </select>
              </label>
            `
          }
          ${
            // `lmm search --tag` (C-3). Offered only where a source actually
            // honours it - NexusMods is the one that does, and a filter that
            // silently narrows nothing is worse than no filter at all. The
            // game's own source map is what answers that, since it is the set
            // /api/v1/search will fan out across.
            !collections &&
            tagsSupported &&
            html`
              <label class="form-field form-field--inline library__control">
                Tags
                <input
                  type="search"
                  name="tag"
                  placeholder="e.g. armour, lore-friendly"
                  value=${searchPage.tags ?? ""}
                  onChange=${(e) => actions.searchPageSetTags(e.currentTarget.value)}
                />
              </label>
            `
          }
          ${
            // Never a dead control: with relevance the only sort on offer there
            // is nothing to choose between, so no select at all - the same rule
            // the tag filter above follows.
            sorts.length > 1 &&
            html`
              <label class="form-field form-field--inline library__control">
                Sort
                <select
                  name="sort"
                  value=${currentSort}
                  onChange=${(e) =>
                    actions.searchPageSetSort(e.currentTarget.value)}
                >
                  ${sorts.map(
                    ([name, label]) =>
                      html`<option key=${name} value=${name}>${label}</option>`,
                  )}
                </select>
              </label>
            `
          }
        </div>

        ${
          collections &&
          hits.length === 0 &&
          html`<p class="empty-state__hint">
            No collections match this search.
          </p>`
        }
        ${
          !collections &&
          hits.length === 0 &&
          skippedNotice !== "" &&
          html`<p
            class="empty-state__hint"
            data-testid="search-skipped-sign-in"
          >
            ${skippedNotice}${" "}
            ${(report.skipped_unauthenticated ?? []).map(
              // issue 582: each source left out for want of a key links to
              // its own key field.
              (id) =>
                html`<${AddAPIKeyButton}
                  key=${id}
                  state=${state}
                  sourceID=${id}
                />`,
            )}
          </p>`
        }
        ${
          !collections &&
          skippedNotice === "" &&
          report.attempted_count === 0 &&
          html`<p class="empty-state__hint">
            None of this game's sources support searching.
          </p>`
        }
        ${
          !collections &&
          hits.length === 0 &&
          (report.warnings ?? []).length === 0 &&
          report.attempted_count !== 0 &&
          html`<p class="empty-state__hint">No results match this search.</p>`
        }
        ${
          collections
            ? html`<${CollectionResultsList} hits=${hits} actions=${actions} />`
            : html`<${SourceResultsList}
                hits=${hits}
                warnings=${report.warnings}
                state=${state}
                actions=${actions}
                detailed=${true}
              />`
        }

        <div class="search-page__pager">
          <button
            type="button"
            class="button"
            disabled=${searchPage.page === 0}
            onClick=${() => actions.searchPageGoTo(searchPage.page - 1)}
          >
            <${Arrow} dir="left" /> Prev
          </button>
          <span class="mono">Page ${searchPage.page + 1}</span>
          <button
            type="button"
            class="button"
            disabled=${!report.has_more}
            onClick=${() => actions.searchPageGoTo(searchPage.page + 1)}
          >
            Next <${Arrow} dir="right" />
          </button>
        </div>
      <//>
    </main>
    ${
      route.mod &&
      html`<${ModPanel}
        modKey=${route.mod}
        contextPath=${searchPagePath(home, searchPage.query, currentSort)}
        rows=${[]}
        visible=${[]}
        catalogRows=${hits}
        route=${route}
        state=${state}
        actions=${actions}
      />`
    }
  `;
}

/** searchPagePath is the ?mod= slide-over's own contextPath on this route -
 * the current search results (?q= and the sort preserved), not home: closing
 * the panel (or stepping through it) must land back on the results the user
 * was browsing, not navigate away from the search page entirely. */
function searchPagePath(home, query, sort) {
  const base = `${home}/search?q=${encodeURIComponent(query)}`;
  return sort && sort !== RELEVANCE
    ? `${base}&sort=${encodeURIComponent(sort)}`
    : base;
}

/** SEARCH_TYPES are the tabs, in order: [type, label]. */
const SEARCH_TYPES = [
  [MODS, "Mods"],
  [COLLECTIONS, "Collections"],
];

/** SearchTypeTabs is the Mods / Collections tab pair (issue 651). Choosing
 * one re-runs the search at page 0 (main.js#searchPageSetType), which also
 * writes ?type= to the URL. Arrow keys, Home and End move between the tabs,
 * as a tablist's do (the reorder modal's pattern), focus following; only
 * the selected tab is in the tab order. Each tab controls the one
 * SearchPanel. */
function SearchTypeTabs({ type, actions }) {
  function onKeyDown(e) {
    const i = SEARCH_TYPES.findIndex(([key]) => key === type);
    const n = SEARCH_TYPES.length;
    let to;
    switch (e.key) {
      case "ArrowRight":
        to = (i + 1) % n;
        break;
      case "ArrowLeft":
        to = (i - 1 + n) % n;
        break;
      case "Home":
        to = 0;
        break;
      case "End":
        to = n - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    const [next] = SEARCH_TYPES[to];
    if (next !== type) actions.searchPageSetType(next);
    requestAnimationFrame(() =>
      document
        .querySelector(`.search-page__tabs [data-type="${next}"]`)
        ?.focus(),
    );
  }
  return html`
    <div class="search-page__tabs" role="tablist" aria-label="Search for">
      ${SEARCH_TYPES.map(
        ([key, label]) =>
          html`<button
            key=${key}
            type="button"
            role="tab"
            class="button button--tab"
            id=${`search-tab-${key}`}
            aria-controls="search-panel"
            data-type=${key}
            data-label=${label}
            aria-selected=${type === key ? "true" : "false"}
            tabindex=${type === key ? "0" : "-1"}
            onKeyDown=${onKeyDown}
            onClick=${() => type !== key && actions.searchPageSetType(key)}
          >
            ${label}
          </button>`,
      )}
    </div>
  `;
}

/** SearchPanel is the tab pair's tabpanel: everything under the tabs, in
 * every state of the page (empty, loading, failed, results), so the tabs'
 * aria-controls always names an element. Labelled by the selected tab.
 * Without tabs (a game with no collection source) it adds nothing: the
 * page's markup is what it was. */
function SearchPanel({ tabbed, type, children }) {
  if (!tabbed) return children;
  return html`<div
    id="search-panel"
    role="tabpanel"
    aria-labelledby=${`search-tab-${type}`}
  >
    ${children}
  </div>`;
}
