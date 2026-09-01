// searchresults.js - shared rendering for source search hits (issue 331): the
// omnibar's "From sources (n)" fan-out section (Mission Control) and the
// dedicated search page's result list both render the same core.SearchHit
// rows the same way, so this is the one place that shape becomes DOM.
//
// Source failures surface as a warning row, never swallowed (design doc
// §Search) - core.SearchReport.Warnings renders alongside the hits, not
// instead of them.

import { html } from "../render.js";
import { navigate } from "../router.js";
import { InlineJob } from "./jobprogress.js";

/** installOrigin is the stable per-mod control key an inline install job
 * morphs on - jobprogress.js's own documented convention
 * ("install:fake/123"). */
export function installOrigin(sourceID, modID) {
  return `install:${sourceID}/${modID}`;
}

/** openSearchResult annotates the current URL with ?mod=source/id - the
 * same slide-over navigation library.js#openRow uses for installed rows,
 * so a source result opens the identical panel (modpanel.js's catalog
 * branch renders it for a hit that is not yet installed). */
export function openSearchResult(hit) {
  const url = new URL(window.location.href);
  url.searchParams.set("mod", `${hit.source_id}/${hit.id}`);
  navigate(url.pathname + url.search);
}

/** SourceResultRow renders one core.SearchHit: name, version, source badge,
 * author, and an inline Install control - or "Installed" when the hit
 * already is (SearchHit.Installed). Install opens the confirm-plan
 * framework's install modal (plan_install.js), the same pipeline every
 * other mutation in this application uses; nothing here mutates directly. */
export function SourceResultRow({ hit, state, actions }) {
  const origin = installOrigin(hit.source_id, hit.id);

  return html`
    <li class="search-result" data-mod=${`${hit.source_id}/${hit.id}`}>
      <button
        type="button"
        class="search-result__name"
        onClick=${() => openSearchResult(hit)}
      >
        ${hit.name}
      </button>
      <span class="mono search-result__version">${hit.version}</span>
      <span class="badge search-result__source" title="Source"
        >${hit.source_id}</span
      >
      ${
        hit.author &&
        html`<span class="search-result__author">${hit.author}</span>`
      }
      ${
        hit.installed
          ? html`<span class="badge badge--good">Installed</span>`
          : html`<${InlineJob}
              origin=${origin}
              state=${state}
              actions=${actions}
            >
              <button
                type="button"
                class="button button--small search-result__install"
                onClick=${() =>
                  actions.openPlan({
                    kind: "install",
                    origin,
                    title: `Install ${hit.name}`,
                    confirmLabel: "Install",
                    options: { source_id: hit.source_id, mod_id: hit.id },
                  })}
              >
                Install
              </button>
            <//>`
      }
    </li>
  `;
}

/** SourceResultsList renders every hit plus per-source warnings. */
export function SourceResultsList({ hits, warnings, state, actions }) {
  return html`
    <ul class="search-results">
      ${(warnings ?? []).map(
        (w) => html`
          <li
            key=${`warn-${w.source_id}`}
            class="search-result search-result--warning"
          >
            <span class="badge badge--warn">${w.source_id}</span>
            ${w.error}
          </li>
        `,
      )}
      ${hits.map(
        (hit) => html`
          <${SourceResultRow}
            key=${`${hit.source_id}/${hit.id}`}
            hit=${hit}
            state=${state}
            actions=${actions}
          />
        `,
      )}
    </ul>
  `;
}
