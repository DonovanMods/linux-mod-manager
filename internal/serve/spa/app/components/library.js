// library.js - Mission Control's spine: the mod table, its filter/sort
// controls, multi-select and the batch-bar shell
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Library").
// Row click navigates into the ?mod= slide-over, wired for real in issue
// 330 (Unit 4). The batch bar, the row-level enable toggle and the ⋯ menu
// are issue 332's own (Unit 6): every multi-mod batch action, the live enabled
// toggle, and per-row update/uninstall/lock-unlock/reorder-here.
//
// The row join (buildRows) and the filter/sort STATE both moved up to
// missioncontrol.js in issue 330: the slide-over's ←/→ stepping needs the
// exact list this table is showing, so this component now renders `visible`
// rather than computing it.

import { html, useEffect, useState } from "../render.js";
import { navigate } from "../router.js";
import { ApiError } from "../api.js";
import {
  formatDate,
  countExternal,
  FILTER_NAMES,
  SORT_NAMES,
} from "../modrows.js";
import { mutationLabel, progressText } from "../progress.js";
import { displayVersion } from "../version.js";
import { AddModsMenu } from "./addmodsmenu.js";

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

/** NoMatches is the library's narrowed-to-nothing state (issue 334's empty-
 * state pass). "No mods match this filter." was true but unhelpful in two
 * ways: it named neither of the two things that could be narrowing the
 * table - the omnibar's text and the Filter dropdown are independent, and
 * either alone can empty it - and it left the way out to be guessed.
 *
 * So it says which narrowing is in force, and when the FILTER is one of
 * them it offers the single click that undoes it. The omnibar's text has
 * no such button on purpose: its input is right there in the top bar with
 * the user's own words in it, and a button that silently emptied a field
 * they are still typing in would be worse than the sentence. */
function NoMatches({ query, filter, onFilterChange }) {
  const text = (query ?? "").trim();
  const filtered = filter !== "all";

  return html`
    <div class="empty-state">
      <p class="empty-state__hint">
        ${
          text && filtered
            ? `No mods match "${text}" under the ${FILTER_LABELS[filter]} filter.`
            : text
              ? `No mods match "${text}".`
              : `No mods are ${FILTER_LABELS[filter].toLowerCase()}.`
        }
      </p>
      ${
        filtered &&
        html`<p class="empty-state__actions">
          <button
            type="button"
            class="button button--small"
            data-action="clear-filter"
            onClick=${() => onFilterChange("all")}
          >
            Show all mods
          </button>
        </p>`
      }
    </div>
  `;
}

/** modOrigin builds the "mod:{source}/{id}:{action}" origin every per-mod
 * control in this application shares (modrows.js#modOriginPattern) - the
 * row toggle, the ⋯ menu's Update/Uninstall, and the batch bar's own
 * per-mod jobs all key off it, which is what lets one row show the SAME
 * inline progress no matter which control started the job it is showing. */
function modOrigin(row, action) {
  return `mod:${row.source_id}/${row.id}:${action}`;
}

export function Library({
  state,
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
  actions,
}) {
  const [selected, setSelected] = useState(() => new Set());
  const [menuKey, setMenuKey] = useState(null);
  const [togglingKey, setTogglingKey] = useState(null);

  // m2, unit 6 fix wave: the ⋯ row menu used to close only by re-clicking
  // ⋯, which left it sitting open over the rest of the page once the user
  // had clearly moved on. modal.js's own outside-click/Escape pattern,
  // applied here - a document-level listener while a menu is actually open,
  // torn down the instant it closes so a hidden menu never leaves a
  // listener attached for the rest of the session.
  useEffect(() => {
    if (menuKey === null) return;
    function handleClick(e) {
      if (!e.target.closest(".row-menu-cell")) setMenuKey(null);
    }
    function handleKeyDown(e) {
      if (e.key === "Escape") setMenuKey(null);
    }
    document.addEventListener("click", handleClick);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("click", handleClick);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [menuKey]);

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

  function selectedRows() {
    return visible.filter((r) => selected.has(r.key));
  }

  async function toggleEnabled(row) {
    setTogglingKey(row.key);
    try {
      await actions.startToggle({
        action: row.enabled ? "disable" : "enable",
        sourceID: row.source_id,
        modID: row.id,
        origin: modOrigin(row, "toggle"),
      });
    } finally {
      setTogglingKey(null);
    }
  }

  // toggleLock is the ⋯ menu's own Lock/Unlock (I1, unit 6 fix wave): the
  // menu closes the instant this is clicked, unlike modpanel.js's
  // ModSettingsControls (which stays on screen and renders its own inline
  // error) - a rejected ApiError here has no control left to show it on, so
  // it becomes a toast instead, mirroring modpanel.js's own await/try/catch
  // rather than the previous fire-and-forget call that left a failure as
  // nothing but an unhandled promise rejection in the console.
  async function toggleLock(row) {
    setMenuKey(null);
    try {
      if (row.locked) await actions.clearModLock(row.source_id, row.id);
      else await actions.setModLock(row.source_id, row.id, "");
    } catch (err) {
      actions.pushToast({
        tone: "failure",
        title: `Couldn't ${row.locked ? "unlock" : "lock"} ${row.name}`,
        detail: err instanceof ApiError ? err.message : String(err),
      });
    }
  }

  // toggleConvert is the ⋯ menu's own `lmm mod convert` (C-3). Same
  // menu-closes-so-a-toast-is-the-only-place-left shape as toggleLock
  // above. Offered only when core.ModListing's tri-state convert_paks is
  // non-null, which is the wire saying pak conversion applies to this mod
  // at all.
  async function toggleConvert(row) {
    setMenuKey(null);
    try {
      await actions.setModConvert(row.source_id, row.id, !row.convert_paks);
    } catch (err) {
      actions.pushToast({
        tone: "failure",
        title: `Couldn't change pak conversion for ${row.name}`,
        detail: err instanceof ApiError ? err.message : String(err),
      });
    }
  }

  function openReorder() {
    actions.openReorderModal({ profileName: state.route.profile });
  }

  function batchEnable(action) {
    const rows = selectedRows();
    if (rows.length === 0) return;
    setSelected(new Set());
    actions.startBatchToggle(
      action,
      rows.map((r) => ({ source_id: r.source_id, id: r.id, name: r.name })),
    );
  }

  // m5: only rows that actually offer an update are worth planning - a
  // selection with no update at all must not even open a modal (the
  // toolbar button below is disabled for exactly that case), and a MIXED
  // selection must silently drop the rows with nothing to update rather
  // than hand kind_updates.go's own planUpdatesKind a mod it can only file
  // under `not_found`.
  function updatableSelectedRows() {
    // issue 269: an EXTERNAL row is excluded even when it HAS an update.
    // ApplyUpdateBatch declines it (core.ReasonExternalNoUpdate) and no
    // choice in this UI changes that, so counting it would enable a button,
    // state a batch size and open a confirm step for work that will never
    // happen - the same defect the deploy dry run had.
    return selectedRows().filter((r) => r.hasUpdate && !r.isExternal);
  }

  function batchUpdate() {
    const rows = updatableSelectedRows();
    if (rows.length === 0) return;
    // m4/I2: the selection is cleared once this batch is actually
    // confirmed (onConfirmed), not here at open time - a Cancel must leave
    // the batch bar (and its own focus) exactly as the user left it.
    actions.openPlan({
      kind: "updates",
      origin: "library:batch-update",
      title: `Update ${rows.length} mod${rows.length === 1 ? "" : "s"}`,
      confirmLabel: "Update",
      options: { mods: rows.map((r) => r.key) },
      onConfirmed: () => setSelected(new Set()),
    });
  }

  function batchUninstall() {
    const rows = selectedRows();
    if (rows.length === 0) return;
    // m4/I2: see batchUpdate's own note - cleared on confirm, not on open.
    actions.openUninstallBatchModal(
      rows.map((r) => ({ source_id: r.source_id, id: r.id, name: r.name })),
      () => setSelected(new Set()),
    );
  }

  function rowMenu(row) {
    const origin = (action) => modOrigin(row, action);
    return html`
      <div class="row-menu">
        ${
          // issue 269: not offered for an external row, for the reason
          // updatableSelectedRows states - and matching the slide-over, which
          // hides Update for the same mod.
          row.hasUpdate &&
          !row.isExternal &&
          html`<button
            type="button"
            class="row-menu__item"
            onClick=${() => {
              setMenuKey(null);
              actions.openPlan({
                kind: "updates",
                origin: origin("update"),
                title: `Update ${row.name}`,
                confirmLabel: "Update",
                options: { mods: [row.key] },
              });
            }}
          >
            Update
          </button>`
        }
        <button
          type="button"
          class="row-menu__item"
          onClick=${() => {
            setMenuKey(null);
            actions.openPlan({
              kind: "uninstall",
              origin: origin("uninstall"),
              title: `Uninstall ${row.name}`,
              confirmLabel: "Uninstall",
              options: { source_id: row.source_id, mod_id: row.id },
            });
          }}
        >
          Uninstall
        </button>
        <button
          type="button"
          class="row-menu__item"
          onClick=${() => toggleLock(row)}
        >
          ${row.locked ? "Unlock" : "Lock"}
        </button>
        ${
          row.convert_paks !== null &&
          row.convert_paks !== undefined &&
          html`<button
            type="button"
            class="row-menu__item"
            data-action="toggle-convert"
            onClick=${() => toggleConvert(row)}
          >
            ${row.convert_paks ? "Disable pak conversion" : "Enable pak conversion"}
          </button>`
        }
        ${
          // issue 365 (b): an EXTERNAL row has no link to move - Steam owns
          // the item where it sits, and core refuses the relink outright
          // (core.ExternalModError). The full mod page has hidden this
          // action since Tier 1 for exactly that reason; this menu still
          // offered it, so the row's own menu and its page disagreed about
          // what the row could do.
          !row.external &&
          html`<button
            type="button"
            class="row-menu__item"
            data-action="relink"
            onClick=${() => {
              setMenuKey(null);
              actions.openPlan({
                kind: "mod_relink",
                origin: origin("relink"),
                title: `Re-link ${row.name}`,
                confirmLabel: "Re-link",
                options: { mod_id: row.id, source_id: row.source_id },
              });
            }}
          >
            Re-link…
          </button>`
        }
        <button
          type="button"
          class="row-menu__item"
          onClick=${() => {
            setMenuKey(null);
            actions.openReorderModal({
              profileName: state.route.profile,
              focusKey: row.key,
            });
          }}
        >
          Reorder here
        </button>
      </div>
    `;
  }

  if (mods === null) {
    if (error) {
      return html`
        <section class="library">
          <h2 class="section-header">Library</h2>
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
        <p class="app-booting">Loading your library…</p>
      </section>
    `;
  }

  if ((mods.mods ?? []).length === 0) {
    return html`
      <section class="library">
        <h2 class="section-header">Library</h2>
        <div class="empty-state">
          <p>No mods installed yet.</p>
          <p class="empty-state__hint">
            Search for a mod once your sources are configured to add your first
            one, or bring in what you already have:
          </p>
          <div class="empty-state__actions">
            <${AddModsMenu}
              route=${state.route}
              actions=${actions}
              state=${state}
            />
          </div>
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

  // issue 269: reported separately from the library count, so "12 mods" is never
  // read as twelve deployments lmm made. Uses an existing class - the
  // colour ratchets forbid a new literal.
  // `mods` here is the core.ModList DOCUMENT, not its array (the prop is
  // passed straight through from Mission Control's state), so the count
  // reads its own "mods" member.
  const externalCount = countExternal(mods?.mods);

  return html`
    <section class="library">
      <div class="library__toolbar">
        <h2 class="section-header">${libraryLabel}</h2>
        ${
          externalCount > 0 &&
          html`<span class="library__live"
            >${`${externalCount} tracked by Steam`}</span
          >`
        }
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
        <${AddModsMenu}
          route=${state.route}
          actions=${actions}
          state=${state}
        />
        <button
          type="button"
          class="button button--small"
          data-action="reorder"
          onClick=${openReorder}
        >
          Reorder…
        </button>
      </div>

      ${
        visible.length === 0
          ? html`<${NoMatches}
              query=${query}
              filter=${filter}
              onFilterChange=${onFilterChange}
            />`
          : html`
              <table class="library__table">
                <thead>
                  <tr>
                    <th class="col--select">Select</th>
                    <th class="col--enabled">Enabled</th>
                    <th class="col--name">Name</th>
                    <th class="col--version">Version</th>
                    <th class="col--author">Author</th>
                    <th class="col--source">Source</th>
                    <th class="col--badges">Badges</th>
                    <th class="col--order">Load order</th>
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
                        data-mod=${row.key}
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
                            aria-label=${`${row.enabled ? "Disable" : "Enable"} ${row.name}`}
                            checked=${row.enabled}
                            disabled=${Boolean(mutation) || togglingKey === row.key}
                            onClick=${(e) => e.stopPropagation()}
                            onChange=${() => toggleEnabled(row)}
                          />
                        </td>
                        <td class="col--name">
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
                        <td class="col--version mono">
                          ${displayVersion(row)}${
                            row.hasUpdate && html` → ${row.updateTarget}`
                          }
                        </td>
                        <td class="col--author">${row.author || "—"}</td>
                        <td class="col--source mono">${row.source_id}</td>
                        <td class="col--badges mod-row__badges">
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
                          ${
                            row.isExternal &&
                            html`<span
                              class="badge"
                              title="Tracked from your Steam subscription - Steam owns this item's files"
                              >Steam</span
                            >`
                          }
                          <span class="badge badge--policy"
                            >${row.update_policy}</span
                          >
                        </td>
                        <td class="col--order mono">${row.loadOrder}</td>
                        <td class="col--method mono">${row.link_method}</td>
                        <td class="col--installed">
                          ${formatDate(row.installed_at)}
                        </td>
                        <td class="col--menu row-menu-cell">
                          <button
                            type="button"
                            class="button button--small"
                            aria-label=${`Actions for ${row.name}`}
                            aria-expanded=${menuKey === row.key ? "true" : "false"}
                            onClick=${() =>
                              setMenuKey(menuKey === row.key ? null : row.key)}
                          >
                            ⋯
                          </button>
                          ${menuKey === row.key && rowMenu(row)}
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
            <button
              type="button"
              class="button"
              data-action="batch-enable"
              onClick=${() => batchEnable("enable")}
            >
              Enable
            </button>
            <button
              type="button"
              class="button"
              data-action="batch-disable"
              onClick=${() => batchEnable("disable")}
            >
              Disable
            </button>
            <button
              type="button"
              class="button"
              data-action="batch-update"
              disabled=${updatableSelectedRows().length === 0}
              title=${
                updatableSelectedRows().length === 0
                  ? "None of the selected mods offer an update"
                  : undefined
              }
              onClick=${batchUpdate}
            >
              Update
            </button>
            <button
              type="button"
              class="button button--danger"
              data-action="batch-uninstall"
              onClick=${batchUninstall}
            >
              Uninstall
            </button>
          </div>
        `
      }
    </section>
  `;
}
