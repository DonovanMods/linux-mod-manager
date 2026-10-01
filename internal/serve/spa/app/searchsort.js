// searchsort.js - the search page's sort vocabulary (issue 503).
//
// Sorting is SERVER-side: GET /api/v1/search?sort= takes one of these names
// (domain.SearchSort) and the report answers with the sort its hits are in
// ("sort") and the sorts worth offering for this result ("sorts_available":
// relevance first, then each of updated/downloads/popular that at least one
// source that answered can order by - the same rule the Tags filter follows:
// never show a control that would do nothing). The names are the wire's, so
// they are spelled here once and shared by the router-facing code (main.js)
// and the page (components/searchpage.js).

/** The default sort, and the one the wire spells by leaving ?sort= off. */
export const RELEVANCE = "relevance";

/** Every sort, in display order, with the label the select shows. The date
 * and count sorts are descending - newest first, most first - which the
 * labels say in plain words rather than as an arrow. */
export const SEARCH_SORTS = [
  [RELEVANCE, "Relevance"],
  ["updated", "Last updated"],
  ["downloads", "Most downloaded"],
  ["popular", "Popular"],
];

/** knownSort returns raw when it names a sort the wire accepts, else
 * relevance - a hand-edited ?sort=bogus must not become a 400 on the page. */
export function knownSort(raw) {
  return SEARCH_SORTS.some(([name]) => name === raw) ? raw : RELEVANCE;
}

/** offeredSorts is the [name, label] pairs worth showing for a report:
 * SEARCH_SORTS filtered to its sorts_available. Relevance is always offered;
 * a report that predates the field offers nothing else. */
export function offeredSorts(report) {
  const available = report?.sorts_available ?? [RELEVANCE];
  return SEARCH_SORTS.filter(
    ([name]) => name === RELEVANCE || available.includes(name),
  );
}

/** effectiveSort is the sort to show selected, and to carry forward, for a
 * report: the requested one when the report offers it, else relevance. A sort
 * no source that answered can order by is never claimed and never re-sent. */
export function effectiveSort(requested, report) {
  return offeredSorts(report).some(([name]) => name === requested)
    ? requested
    : RELEVANCE;
}
