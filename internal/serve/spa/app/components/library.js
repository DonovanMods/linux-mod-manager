// library.js - Mission Control's spine: the mod table, its filter/sort
// controls, multi-select and the batch-bar shell
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Library").
// Row click navigates into the ?mod= slide-over, wired for real in issue
// 330 (Unit 4); its own multi-mod batch actions and the row-level enable
// toggle stay disabled - the batch bar is Unit 6's (reorder/profiles/health
// repair/update-batch), and moving a single-row enable/disable/uninstall
// action onto this table too would duplicate the slide-over's own affordance
// for no reader-visible benefit yet, in a unit already touching every mod-
// mutation surface once.
//
// The row join (buildRows) and the filter/sort STATE both moved up to
// missioncontrol.js in issue 330: the slide-over's ←/→ stepping needs the
// exact list this table is showing, so this component now renders `visible`
// rather than computing it.

import { html, useState } from "../render.js";
import { navigate } from "../router.js";
import { formatDate, FILTER_NAMES, SORT_NAMES } from "../modrows.js";
import { mutationLabel, progressText } from "../progress.js";
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
  visible,
  filter,
  sort,
  onFilterChange,
  onSortChange,
  mutations,
  liveActivity,
  query,
  error,
  onRetry,
}) {
  const [selected, setSelected] = useState(() => new Set());

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
        ${
          liveActivity &&
          html`<span class="library__live" role="status">${liveActivity}</span>`
        }
        <label class="library__control">
          Filter
          <select
            name="filter"
            value=${filter}
            onChange=${(e) => onFilterChange(e.currentTarget.value)}
          >
            ${FILTER_NAMES.map((f) => html`<option value=${f}>${FILTER_LABELS[f]}</option>`)}
          </select>
        </label>
        <label class="library__control">
          Sort
          <select
            name="sort"
            value=${sort}
            onChange=${(e) => onSortChange(e.currentTarget.value)}
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
                    <th class="col--select">Select</th>
                    <th class="col--enabled">Enabled</th>
                    <th>Name</th>
                    <th>Version</th>
                    <th class="col--author">Author</th>
                    <th class="col--source">Source</th>
                    <th>Badges</th>
                    <th>Load order</th>
                    <th class="col--method">Method</th>
                    <th class="col--installed">Installed</th>
                    <th class="col--menu"></th>
                  </tr>
                </thead>
                <tbody>
                  ${visible.map((row) => {
                    // issue 330 carry-3's "move onto the rows it concerns":
                    // a mutation this session started against THIS mod
                    // (modrows.js#runningMutations, keyed the same
                    // "source:id" way modKey() is) shows its own live text
                    // here instead of the library's shared header line.
                    const mutation = mutations?.get(row.key);
                    return html`
                      <tr
                        key=${row.key}
                        class="mod-row ${selected.has(row.key) ? "mod-row--selected" : ""}"
                      >
                        <td class="col--select">
                          <input
                            type="checkbox"
                            aria-label=${`Select ${row.name} for batch actions`}
                            checked=${selected.has(row.key)}
                            onClick=${(e) => e.stopPropagation()}
                            onChange=${() => toggleSelect(row.key)}
                          />
                        </td>
                        <td class="col--enabled">
                          <input
                            type="checkbox"
                            aria-label=${`Enable ${row.name}`}
                            checked=${row.enabled}
                            disabled
                            title=${NOT_YET}
                          />
                        </td>
                        <td>
                          <button
                            type="button"
                            class="mod-row__name"
                            onClick=${() => openRow(row)}
                          >
                            ${row.name}
                          </button>
                          ${
                            mutation &&
                            html`<span class="mod-row__live" role="status">
                              ${[
                                mutationLabel(mutation.summary.kind),
                                progressText(mutation.frame),
                              ]
                                .filter(Boolean)
                                .join(" · ")}
                            </span>`
                          }
                        </td>
                        <td class="mono">
                          ${row.version}${row.hasUpdate && html` → ${row.updateTarget}`}
                        </td>
                        <td class="col--author">${row.author || "—"}</td>
                        <td class="col--source mono">${row.source_id}</td>
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
                        <td class="col--method mono">${row.link_method}</td>
                        <td class="col--installed">
                          ${formatDate(row.installed_at)}
                        </td>
                        <td class="col--menu">
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
                    `;
                  })}
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
