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

/** formatDownloads renders a download COUNT the way the search page's own
 * "star/download counts" bullet (design doc §Search) means it: compact,
 * thousands-grouped, never a raw unformatted integer. Zero/undefined
 * renders nothing - "0 downloads" on a hit whose source never reports the
 * figure is misleading, not merely uninteresting. */
function formatDownloads(n) {
  if (!n) return "";
  return `${n.toLocaleString()} downloads`;
}

/** SourceResultRow renders one core.SearchHit: name, version, source badge,
 * author, and an inline Install control - or "Installed" when the hit
 * already is (SearchHit.Installed). Install opens the confirm-plan
 * framework's install modal (plan_install.js), the same pipeline every
 * other mutation in this application uses; nothing here mutates directly.
 *
 * detailed (design doc §Search's search-PAGE bullet: "source badges, star/
 * download counts, summaries") adds the category badge, the download
 * count, and a one-line summary - the omnibar's inline fan-out stays
 * terse (its own bullet names none of these), so this is opt-in rather
 * than always-on for a component both surfaces share. */
export function SourceResultRow({ hit, state, actions, detailed }) {
  const origin = installOrigin(hit.source_id, hit.id);
  const downloads = detailed ? formatDownloads(hit.downloads) : "";

  return html`
    <li
      class="search-result ${detailed ? "search-result--detailed" : ""}"
      data-mod=${`${hit.source_id}/${hit.id}`}
    >
      <div class="search-result__row">
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
          detailed &&
          hit.category &&
          html`<span class="badge search-result__category"
            >${hit.category}</span
          >`
        }
        ${
          hit.author &&
          html`<span class="search-result__author">${hit.author}</span>`
        }
        ${
          downloads &&
          html`<span class="search-result__downloads">${downloads}</span>`
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
      </div>
      ${
        detailed &&
        hit.summary &&
        html`<p class="search-result__summary">${hit.summary}</p>`
      }
    </li>
  `;
}

/** SourceResultsList renders every hit plus per-source warnings. detailed
 * forwards to SourceResultRow - see its own doc comment. */
export function SourceResultsList({
  hits,
  warnings,
  state,
  actions,
  detailed,
}) {
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
            detailed=${detailed}
          />
        `,
      )}
    </ul>
  `;
}
