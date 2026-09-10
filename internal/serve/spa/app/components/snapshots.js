// snapshots.js - the Snapshots card (issue 350): list, create, restore, delete.
//
// It is NOT one of cards.js's attention cards. Those render only when they
// have something to say, and a card absent entirely is itself the "nothing
// needs you here" signal. Snapshots are the opposite kind of surface: a
// safety net whose value is knowing it is there, and whose primary action
// ("Snapshot now") is available precisely when there is nothing to show. So
// it lives below the library, always rendered, out of the way of the
// attention row at the top of the densest screen in the product.
//
// Create and delete are api_snapshots.go's single-step writes, called
// directly the way profilesmodal.js calls its own - they answer with the
// document they produced, so there is nothing to preview and nothing to
// watch. RESTORE is the destructive, four-stage one, and it goes through
// the shared confirm-plan framework like every other mutation in this UI.

import { html, useState } from "../render.js";
import { createSnapshot, deleteSnapshot } from "../api.js";
import { relativeTime } from "../relativetime.js";
import { formatBytes } from "../progress.js";
import { InlineJob } from "./jobprogress.js";

// SNAPSHOT_CREATE_ORIGIN is the card's own "Snapshot now" control. It is
// not a job origin (a create is synchronous) but it keeps the naming
// convention every other control in this UI follows.
const SNAPSHOT_CREATE_ORIGIN = "snapshot:create";

/** restoreOrigin is one snapshot row's own Restore control, so the row the
 * user clicked is the one that morphs into the running restore. */
function restoreOrigin(name) {
  return `snapshot:${name}:restore`;
}

/** SnapshotsCard renders the game's core.SnapshotListing. */
export function SnapshotsCard({ state, actions }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [confirmingDelete, setConfirmingDelete] = useState(null);

  const listing = state.snapshots;
  const rows = listing?.snapshots ?? [];
  const fetchError = state.fetchErrors?.snapshots;
  const context = { game: state.route.game, profile: state.route.profile };

  async function snapshotNow() {
    setBusy(true);
    setError(null);
    try {
      // No name: the server applies core's own shared default, which is
      // why this button needs no text input (api.js#createSnapshot).
      await createSnapshot("", context);
      await actions.reloadSnapshots();
    } catch (err) {
      setError(String(err?.message ?? err));
    } finally {
      setBusy(false);
    }
  }

  async function remove(name) {
    setBusy(true);
    setError(null);
    try {
      await deleteSnapshot(name, context);
      setConfirmingDelete(null);
      await actions.reloadSnapshots();
    } catch (err) {
      setError(String(err?.message ?? err));
    } finally {
      setBusy(false);
    }
  }

  function restore(name) {
    actions.openPlan({
      kind: "snapshot_restore",
      origin: restoreOrigin(name),
      title: `Restore ${name}`,
      confirmLabel: "Restore",
      options: { snapshot: name },
    });
  }

  return html`
    <section class="snapshots" data-testid="snapshots-card">
      <div class="card card--snapshots">
        <h2 class="card__title">${`⏱ Snapshots (${rows.length})`}</h2>
        <p class="card__meta">
          A named point you can bring this game back to. The mod files stay in
          the cache, so a snapshot costs kilobytes.
        </p>
        ${
          fetchError &&
          html`<p class="card__error">
            Couldn't list snapshots: ${fetchError}
          </p>`
        }
        ${error && html`<p class="card__error">${error}</p>`}
        ${
          rows.length === 0
            ? html`<p class="card__meta" data-testid="snapshots-empty">
                No snapshots yet.
              </p>`
            : html`
                <ul class="card__list">
                  ${rows.map(
                    (row) => html`
                      <${SnapshotRow}
                        key=${row.name}
                        row=${row}
                        state=${state}
                        actions=${actions}
                        busy=${busy}
                        confirming=${confirmingDelete === row.name}
                        onRestore=${() => restore(row.name)}
                        onAskDelete=${() => setConfirmingDelete(row.name)}
                        onCancelDelete=${() => setConfirmingDelete(null)}
                        onDelete=${() => remove(row.name)}
                      />
                    `,
                  )}
                </ul>
              `
        }
        <div class="card__actions">
          <button
            type="button"
            class="button"
            data-action="snapshot-now"
            data-origin=${SNAPSHOT_CREATE_ORIGIN}
            disabled=${busy}
            onClick=${snapshotNow}
          >
            Snapshot now
          </button>
        </div>
      </div>
    </section>
  `;
}

/** SnapshotRow is one snapshot: what it is, when it was taken, and its own
 * Restore and Delete affordances. Delete confirms INLINE, in place on the
 * row, because "modals stack at most one deep" (design doc §Modals) leaves
 * no room for a nested confirm dialog - the same substitute
 * profilesmodal.js's own rows use. */
function SnapshotRow({
  row,
  state,
  actions,
  busy,
  confirming,
  onRestore,
  onAskDelete,
  onCancelDelete,
  onDelete,
}) {
  // ONE string per cell rather than adjacent interpolations: htm collapses
  // the whitespace between them and would fuse the words together (the trap
  // cards.js#conflictLabel documents).
  const taken = relativeTime(row.created_at) || "just now";
  const detail = `${taken} · ${row.mods} mod${row.mods === 1 ? "" : "s"} · ${row.originals} original${row.originals === 1 ? "" : "s"} · ${formatBytes(row.size_bytes) || "0 B"}`;
  const label = row.auto ? `${row.name} (automatic)` : row.name;

  if (confirming) {
    return html`
      <li class="card__row">
        <span class="card__row-name">${`Delete ${row.name}?`}</span>
        <span class="card__row-detail"
          >The stored originals are kept - they are the only copy of the files
          lmm replaced.</span
        >
        <button
          type="button"
          class="button button--small button--danger"
          data-action="snapshot-delete-confirm"
          disabled=${busy}
          onClick=${onDelete}
        >
          Delete
        </button>
        <button
          type="button"
          class="button button--small"
          data-action="snapshot-delete-cancel"
          onClick=${onCancelDelete}
        >
          Cancel
        </button>
      </li>
    `;
  }

  return html`
    <li class="card__row">
      <span class="card__row-name" title=${label}>${label}</span>
      <span class="card__row-detail" title=${detail}>${detail}</span>
      <${InlineJob}
        origin=${restoreOrigin(row.name)}
        state=${state}
        actions=${actions}
      >
        <button
          type="button"
          class="button button--small"
          data-action="snapshot-restore"
          onClick=${onRestore}
        >
          Restore…
        </button>
      <//>
      <button
        type="button"
        class="button button--small"
        data-action="snapshot-delete"
        disabled=${busy}
        onClick=${onAskDelete}
      >
        Delete
      </button>
    </li>
  `;
}
