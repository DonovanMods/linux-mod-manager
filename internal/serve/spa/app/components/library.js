// library.js - Mission Control's spine: the mod table, its filter/sort
// controls, multi-select and the batch-bar shell
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Library").
// Every mutation affordance here is present and disabled - Unit 3 wires the
// confirm-modal framework they submit through. Row click navigates into the
// ?mod= slide-over annotation; the panel itself is a placeholder until
// Unit 4.

import { html, useMemo, useState } from "../render.js";
import { navigate } from "../router.js";
import {
  buildRows,
  filterRows,
  sortRows,
  FILTER_NAMES,
  SORT_NAMES,
} from "../modrows.js";
import { NOT_YET } from "../ui.js";

const FILTER_LABELS = {
  all: "All",
  enabled: "Enabled",
  updatable: "Updatable",
  unhealthy: "Unhealthy",
};

const SORT_LABELS = {
  "load-order": "Load order",
  name: "Name",
  recent: "Recently installed",
};

export function Library({
  mods,
  updates,
  health,
  conflicts,
  query,
  error,
  onRetry,
}) {
  const [filter, setFilter] = useState("all");
  const [sort, setSort] = useState("load-order");
  const [selected, setSelected] = useState(() => new Set());

  const rows = useMemo(
    () =>
      buildRows(
        mods?.mods ?? [],
        updates?.updates ?? [],
        health?.result?.findings ?? [],
        conflicts?.conflicts ?? [],
      ),
    [mods, updates, health, conflicts],
  );

  const visible = useMemo(() => {
    const q = (query ?? "").trim().toLowerCase();
    const matched = q
      ? rows.filter((r) => r.name.toLowerCase().includes(q))
      : rows;
    return sortRows(filterRows(matched, filter), sort);
  }, [rows, filter, sort, query]);

  function toggleSelect(key) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  // A plain (pushed) navigation, not a replace: opening the slide-over is a
  // new place in history on purpose, so Back closes it (router.js's own
  // doc comment) rather than leaving Mission Control entirely.
  function openRow(row) {
    const url = new URL(window.location.href);
    url.searchParams.set("mod", `${row.source_id}/${row.id}`);
    navigate(url.pathname + url.search);
  }

  if (mods === null) {
    if (error) {
      return html`
        <section class="library">
          <p class="section-header">Library</p>
          <div class="empty-state empty-state--error">
            <p>Couldn't load your library: ${error}</p>
            <button
              type="button"
              class="button button--small"
              onClick=${onRetry}
            >
              Retry
            </button>
          </div>
        </section>
      `;
    }
    return html`
      <section class="library">
        <p class="app-booting">Loading library&#8230;</p>
      </section>
    `;
  }

  if ((mods.mods ?? []).length === 0) {
    return html`
      <section class="library">
        <p class="section-header">Library</p>
        <div class="empty-state">
          <p>No mods installed yet.</p>
          <p class="empty-state__hint">
            Search for a mod once your sources are configured to add your first
            one.
          </p>
        </div>
      </section>
    `;
  }

  // The design's own name for this count while the omnibar is narrowing the
  // table ("In your library (n)" - §Search); the plain filter/sort controls
  // keep the section's usual "Library (n)" heading. Either way it counts
  // `visible`, not `rows` - M3: the header used to ignore the Filter
  // dropdown entirely, staying "Library (3)" under a filtered-to-one table.
  const libraryLabel = (query ?? "").trim()
    ? `In your library (${visible.length})`
    : `Library (${visible.length})`;

  return html`
    <section class="library">
      <div class="library__toolbar">
        <p class="section-header">${libraryLabel}</p>
        <label class="library__control">
          Filter
          <select
            name="filter"
            value=${filter}
            onChange=${(e) => setFilter(e.currentTarget.value)}
          >
            ${FILTER_NAMES.map((f) => html`<option value=${f}>${FILTER_LABELS[f]}</option>`)}
          </select>
        </label>
        <label class="library__control">
          Sort
          <select
            name="sort"
            value=${sort}
            onChange=${(e) => setSort(e.currentTarget.value)}
          >
            ${SORT_NAMES.map((s) => html`<option value=${s}>${SORT_LABELS[s]}</option>`)}
          </select>
        </label>
      </div>

      ${
        visible.length === 0
          ? html`<p class="empty-state__hint">No mods match this filter.</p>`
          : html`
              <table class="library__table">
                <thead>
                  <tr>
                    <th class="library__table-select"></th>
                    <th class="library__table-enabled"></th>
                    <th>Name</th>
                    <th>Version</th>
                    <th>Badges</th>
                    <th>Load order</th>
                    <th class="library__table-menu"></th>
                  </tr>
                </thead>
                <tbody>
                  ${visible.map(
                    (row) => html`
                      <tr
                        key=${row.key}
                        class="mod-row ${selected.has(row.key) ? "mod-row--selected" : ""}"
                      >
                        <td>
                          <input
                            type="checkbox"
                            checked=${selected.has(row.key)}
                            onClick=${(e) => e.stopPropagation()}
                            onChange=${() => toggleSelect(row.key)}
                          />
                        </td>
                        <td>
                          <input
                            type="checkbox"
                            checked=${row.enabled}
                            disabled
                            title=${NOT_YET}
                          />
                        </td>
                        <td class="mod-row__name" onClick=${() => openRow(row)}>
                          ${row.name}
                        </td>
                        <td class="mono">
                          ${row.version}${row.hasUpdate && html` → ${row.updateTarget}`}
                        </td>
                        <td class="mod-row__badges">
                          ${
                            row.hasUpdate &&
                            html`<span
                              class="badge badge--good"
                              title="Update available"
                              >⬆</span
                            >`
                          }
                          ${
                            row.hasHealthIssue &&
                            html`<span
                              class="badge badge--warn"
                              title="Health issue"
                              >⚠</span
                            >`
                          }
                          ${
                            row.hasConflict &&
                            html`<span
                              class="badge badge--danger"
                              title="File conflict"
                              >⇄</span
                            >`
                          }
                          ${
                            row.locked &&
                            html`<span
                              class="badge"
                              title="Locked to ${row.locked_version}"
                              >🔒</span
                            >`
                          }
                          <span class="badge badge--policy"
                            >${row.update_policy}</span
                          >
                        </td>
                        <td class="mono">${row.loadOrder}</td>
                        <td>
                          <button
                            type="button"
                            class="button button--small"
                            disabled
                            title=${NOT_YET}
                          >
                            ⋯
                          </button>
                        </td>
                      </tr>
                    `,
                  )}
                </tbody>
              </table>
            `
      }
      ${
        selected.size > 0 &&
        html`
          <div class="batch-bar">
            <span>${selected.size} selected</span>
            <button type="button" class="button" disabled title=${NOT_YET}>
              Enable
            </button>
            <button type="button" class="button" disabled title=${NOT_YET}>
              Disable
            </button>
            <button type="button" class="button" disabled title=${NOT_YET}>
              Update
            </button>
            <button
              type="button"
              class="button button--danger"
              disabled
              title=${NOT_YET}
            >
              Uninstall
            </button>
          </div>
        `
      }
    </section>
  `;
}
