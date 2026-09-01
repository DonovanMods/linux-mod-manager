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
// (main.js#runSearchPage); category/source filtering and sort are applied
// CLIENT-SIDE over the CURRENT page's own hits - every SearchHit already
// carries category/source_id/downloads, so narrowing or reordering what is
// already on screen needs no round trip.

import { html, useMemo, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { SourceResultsList } from "./searchresults.js";

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
  // order on every subsequent render.
  const [category, setCategory] = useState("");
  const [source, setSource] = useState("");
  const [sort, setSort] = useState("relevance");

  const searchPage = state.searchPage;
  const query = (route.q ?? "").trim();
  const matches = searchPage && searchPage.query === query;
  const report = matches ? searchPage.report : null;
  const hits = report?.mods ?? [];

  const categories = useMemo(
    () => [...new Set(hits.map((h) => h.category).filter(Boolean))].sort(),
    [hits],
  );
  const sourceIDs = useMemo(
    () => [...new Set(hits.map((h) => h.source_id))].sort(),
    [hits],
  );
  const filtered = useMemo(() => {
    let out = hits;
    if (category) out = out.filter((h) => h.category === category);
    if (source) out = out.filter((h) => h.source_id === source);
    return out;
  }, [hits, category, source]);
  const sorted = useMemo(() => {
    if (sort === "downloads") {
      return [...filtered].sort(
        (a, b) => (b.downloads ?? 0) - (a.downloads ?? 0),
      );
    }
    if (sort === "name") {
      return [...filtered].sort((a, b) => a.name.localeCompare(b.name));
    }
    return filtered;
  }, [filtered, sort]);

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
        <p class="app-booting">Searching&#8230;</p>
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

  return html`
    ${header}
    <main class="app-main search-page" data-hydrated="true">
      <div class="search-page__toolbar">
        <p class="section-header">
          Results for “${searchPage.query}” (${sorted.length})
        </p>
        ${
          categories.length > 0 &&
          html`
            <label class="library__control">
              Category
              <select
                name="category"
                value=${category}
                onChange=${(e) => setCategory(e.currentTarget.value)}
              >
                <option value="">All</option>
                ${categories.map(
                  (c) => html`<option key=${c} value=${c}>${c}</option>`,
                )}
              </select>
            </label>
          `
        }
        ${
          sourceIDs.length > 1 &&
          html`
            <label class="library__control">
              Source
              <select
                name="source"
                value=${source}
                onChange=${(e) => setSource(e.currentTarget.value)}
              >
                <option value="">All</option>
                ${sourceIDs.map(
                  (s) => html`<option key=${s} value=${s}>${s}</option>`,
                )}
              </select>
            </label>
          `
        }
        <label class="library__control">
          Sort
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
  `;
}
