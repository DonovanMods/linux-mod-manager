// collectionresults.js - the search page's Collections tab (issue 651): one
// row per core.CollectionHit, with what choosing one needs (curator, mod
// count, revision, endorsements, downloads, size, the adult flag, the
// summary), a link to the collection's page, and Import.
//
// Import opens the existing "Import a collection" dialog
// (importcollectionmodal.js) with the hit's URL in its NexusMods field, so
// the import plans exactly as one typed there does - the same reference
// `lmm profile import --nexus-collection <url>` takes - and the user can
// name the profile before the plan.
//
// The rows lay out in the mod results' own grid (.search-results, issue
// 520), so the two tabs line up the same way: the name column, then the
// revision where a mod's version goes, the badges, the curator, the age,
// the counts and the actions.

import { html } from "../render.js";
import { formatBytes } from "../progress.js";
import { absoluteTime, updatedPhrase } from "../relativetime.js";
import { ModPageLink } from "./modpagelink.js";

/** counts is the row's numbers, compact: "4,893 endorsements · 767,891
 * downloads". A zero count is left out, as the mod rows leave a zero
 * download count out. */
function counts(hit) {
  const parts = [];
  if (hit.endorsements)
    parts.push(
      `${hit.endorsements.toLocaleString()} endorsement${hit.endorsements === 1 ? "" : "s"}`,
    );
  if (hit.downloads) parts.push(`${hit.downloads.toLocaleString()} downloads`);
  return parts.join(" · ");
}

/** CollectionResultRow renders one hit. */
export function CollectionResultRow({ hit, actions }) {
  const updated = updatedPhrase(hit.updated_at);
  const size = formatBytes(hit.size_bytes);
  return html`
    <li
      class="search-result search-result--detailed"
      data-collection=${`${hit.source_id}/${hit.id}`}
    >
      <div class="search-result__row">
        <span class="search-result__name">${hit.name}</span>
        <span class="mono search-result__version" title="Latest revision"
          >rev ${hit.revision}</span
        >
        <span class="search-result__badges">
          <span class="badge search-result__source" title="Source"
            >${hit.source_id}</span
          >
          <span class="badge search-result__category"
            >${hit.mod_count}
            mod${hit.mod_count === 1 ? "" : "s"}${
              size ? ` · ${size}` : ""
            }</span
          >
          ${
            hit.adult &&
            html`<span class="badge badge--warn" data-testid="collection-adult"
              >Adult</span
            >`
          }
        </span>
        <span class="search-result__author">${hit.author ?? ""}</span>
        <span
          class="search-result__updated"
          title=${updated ? absoluteTime(hit.updated_at) : undefined}
          >${updated}</span
        >
        <span class="search-result__downloads">${counts(hit)}</span>
        <span class="search-result__actions">
          <${ModPageLink}
            className="search-result__page"
            url=${hit.url}
            sourceID=${hit.source_id}
            modName=${hit.name}
          />
          <button
            type="button"
            class="button button--small"
            data-testid="collection-import"
            aria-label=${`Import ${hit.name}`}
            onClick=${() =>
              actions.openImportCollectionModal({
                prefill: { nexus_collection: hit.url },
                openerSelector: `[data-collection="${hit.source_id}/${hit.id}"] [data-testid="collection-import"]`,
              })}
          >
            Import…
          </button>
        </span>
      </div>
      ${hit.summary && html`<p class="search-result__summary">${hit.summary}</p>`}
    </li>
  `;
}

/** CollectionResultsList renders every hit in the mod results' grid. */
export function CollectionResultsList({ hits, actions }) {
  return html`
    <ul class="search-results search-results--detailed">
      ${hits.map(
        (hit) =>
          html`<${CollectionResultRow}
            key=${`${hit.source_id}/${hit.id}`}
            hit=${hit}
            actions=${actions}
          />`,
      )}
    </ul>
  `;
}
