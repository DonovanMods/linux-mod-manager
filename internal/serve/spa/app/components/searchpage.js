// searchpage.js - the dedicated search page at /g/{game}/{profile}/search?q=
// (issue 331, design doc §Search: "the escape hatch for heavy browsing - source
// badges, star/download counts, summaries, category/source filters, sort,
// pagination"). Reached from a deep link, or later a "search sources ↵"/
// "See all results" affordance from the omnibar - the model for "pages that
// earn it" (design doc), so unlike Mission Control's inline fan-out this one
// actually leaves home: its own minimal header with a Back link, matching
// fullmodpage.js's own pattern for the same reason.
//
// issue 331's own pagination fetches a fresh page from the server
// (main.js#runSearchPage). Category/source filtering moved SERVER-SIDE
// there too (Important 1b, unit 5 fix wave): a filter change re-queries
// page 0 with that option, so the count this page renders answers the
// CATALOG for that filter, not a client-side slice of one page. Sort stays
// client-side and page-local - it only reorders what a page already holds
// (every SearchHit already carries its own downloads/name), which needs no
// round trip; its own label says so (Important 1c).

import { html, useMemo, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { SourceResultsList } from "./searchresults.js";
import { ModPanel } from "./modpanel.js";

function BackLink({ to }) {
  return html`<a
    class="mod-page__back"
    href=${to}
    onClick=${(e) => {
      e.preventDefault();
      navigate(to);
    }}
    >← Back to library</a
  >`;
}

export function SearchPage({ state, route, onThemeChange, actions }) {
  // Hooks run unconditionally, before any of the branches below return -
  // missioncontrol.js's own rule, for the same reason: a component that
  // calls fewer hooks on one render than another corrupts Preact's hook
  // order on every subsequent render. Sort is the only filter still LOCAL
  // to this component - category/source live in state.searchPage, since a
  // filter change now re-fetches (main.js#runSearchPage).
  const [sort, setSort] = useState("relevance");

  const searchPage = state.searchPage;
  const query = (route.q ?? "").trim();
  const matches = searchPage && searchPage.query === query;
  const report = matches ? searchPage.report : null;
  const hits = report?.mods ?? [];
  const facets = matches ? searchPage.facets : null;

  const sorted = useMemo(() => {
    if (sort === "downloads") {
      return [...hits].sort((a, b) => (b.downloads ?? 0) - (a.downloads ?? 0));
    }
    if (sort === "name") {
      return [...hits].sort((a, b) => a.name.localeCompare(b.name));
    }
    return hits;
  }, [hits, sort]);

  const home = contextPath(route.game, route.profile);
  const header = html`
    <header class="app-bar">
      <span class="app-bar__brand">LMM</span>
      <${BackLink} to=${home} />
      <button
        type="button"
        class="theme-toggle"
        onClick=${() => onThemeChange(cycleTheme())}
      >
        Theme: ${currentTheme()}
      </button>
    </header>
  `;

  if (!query) {
    return html`
      ${header}
      <main class="app-main search-page">
        <p class="empty-state__hint">
          Search from the omnibar (or a deep link with ?q=) to browse results
          here.
        </p>
      </main>
    `;
  }

  if (!matches || searchPage.status === "loading") {
    return html`${header}
      <main class="app-main search-page">
        <p class="app-booting">Searching…</p>
      </main>`;
  }

  if (searchPage.status === "error") {
    return html`
      ${header}
      <main class="app-main search-page">
        <p class="app-error">Couldn't search: ${searchPage.error}</p>
      </main>
    `;
  }

  // The header names what its own number IS (Important 1a): a page's own
  // hit count, never a bare "(n)" that reads as the catalog's total - there
  // is no catalog total on the wire (TotalResults is this page's own
  // pre-cap merge). "more available" is the has_more cue a reader needs to
  // know a Next page exists at all.
  const pageSummary = `Page ${searchPage.page + 1} · ${sorted.length} on this page${report.has_more ? " · more available" : ""}`;

  return html`
    ${header}
    <main class="app-main search-page" data-hydrated="true">
      <div class="search-page__toolbar">
        <p class="section-header">
          Results for “${searchPage.query}” — ${pageSummary}
        </p>
        ${
          (facets?.categories.length ?? 0) > 0 &&
          html`
            <label class="library__control">
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
          (facets?.sourceIDs.length ?? 0) > 1 &&
          html`
            <label class="library__control">
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
        <label class="library__control">
          Sort (on this page)
          <select
            name="sort"
            value=${sort}
            onChange=${(e) => setSort(e.currentTarget.value)}
          >
            <option value="relevance">Relevance</option>
            <option value="downloads">Downloads</option>
            <option value="name">Name</option>
          </select>
        </label>
      </div>

      ${
        report.attempted_count === 0 &&
        html`<p class="empty-state__hint">
          None of this game's sources support searching.
        </p>`
      }
      ${
        sorted.length === 0 &&
        (report.warnings ?? []).length === 0 &&
        report.attempted_count !== 0 &&
        html`<p class="empty-state__hint">No results match this search.</p>`
      }

      <${SourceResultsList}
        hits=${sorted}
        warnings=${report.warnings}
        state=${state}
        actions=${actions}
        detailed=${true}
      />

      <div class="search-page__pager">
        <button
          type="button"
          class="button button--small"
          disabled=${searchPage.page === 0}
          onClick=${() => actions.searchPageGoTo(searchPage.page - 1)}
        >
          ← Prev
        </button>
        <span class="mono">Page ${searchPage.page + 1}</span>
        <button
          type="button"
          class="button button--small"
          disabled=${!report.has_more}
          onClick=${() => actions.searchPageGoTo(searchPage.page + 1)}
        >
          Next →
        </button>
      </div>
    </main>
    ${
      route.mod &&
      html`<${ModPanel}
        modKey=${route.mod}
        contextPath=${searchPagePath(home, searchPage.query)}
        rows=${[]}
        visible=${[]}
        catalogRows=${sorted}
        route=${route}
        state=${state}
        actions=${actions}
      />`
    }
  `;
}

/** searchPagePath is the ?mod= slide-over's own contextPath on this route -
 * the current search results (?q= preserved), not home: closing the panel
 * (or stepping through it) must land back on the results the user was
 * browsing, not navigate away from the search page entirely. */
function searchPagePath(home, query) {
  return `${home}/search?q=${encodeURIComponent(query)}`;
}
