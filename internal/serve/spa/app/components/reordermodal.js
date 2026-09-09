// reordermodal.js - the reorder modal: drag-and-drop load order with a live
// conflict preview (docs/plans/2026-08-31-serve-spa-design.md §Modals:
// "reorder (drag-and-drop load order with live conflict preview; reachable
// from Conflicts card and library)", issue 332).
//
// It lives in the SAME modal slot the confirm-plan framework uses
// (store.js's own doc comment: "a later unit's reorder or profiles modal is
// another shape in this same slot, not another slot") but is NOT a
// Plan/Apply mutation: reorder is one of api_profiles.go's sanctioned
// single-step writes, with nothing to preview through a Plan document - the
// preview here is a SEPARATE read (GET /api/v1/conflicts?order=), live
// against the order this modal is currently proposing, not a frozen plan
// handle. So this component talks to api.js directly rather than through
// actions.openPlan/confirmPlan.
//
// Two reordering paths, both wired to the SAME state update, per WEBUI.md's
// "no hover-only or precision-pointer-dependent access to critical
// functionality": a pointer drag (plain mousedown/mouseenter/mouseup - see
// this file's own note on why NOT the native HTML5 Drag and Drop API), and
// a button path (move up/down, first/last) that needs nothing but Tab and
// Enter/Space.
//
// The winner rule is never recomputed here - every "which mod wins" fact
// rendered below is read verbatim off core.ConflictReport's own
// load_order_winner/stale fields (api_profiles.go's own doc comment: "Do
// not compute the winner in JS").

import { html, useEffect, useMemo, useRef, useState } from "../render.js";
import { Modal } from "./modal.js";
import { conflictsForOrder, reorderProfile } from "../api.js";
import { modKey } from "../modrows.js";
import { staleWinnerNote } from "../conflicts.js";

// PREVIEW_DEBOUNCE_MS mirrors main.js's own debounced fetches (the omnibar
// fan-out has none - it fires on Enter - but the search page's category/
// source filters and this modal's own order edits share the same shape:
// many rapid edits, one request per settled state). 200ms is short enough
// that a preview never feels stale to a person watching the list settle,
// long enough that a five-move drag doesn't fire five requests.
const PREVIEW_DEBOUNCE_MS = 200;

/** buildInitialOrder seeds the working order from the library's own
 * ModList - already in the profile's load order (modrows.js's own doc
 * comment on buildRows) - as the fully-qualified "source:id" keys
 * POST .../reorder and GET ?order= both take, never a bare id (api_profiles
 * .go: bare ids are only for a HUMAN typing one; a machine-built list has no
 * reason to risk the ambiguous case). */
function buildInitialOrder(mods) {
  return (mods ?? []).map(modKey);
}

/** conflictsByPath indexes a ConflictReport's rows by path - the join this
 * modal's preview table needs (current vs proposed winner for the SAME
 * path), since a proposed order's response is not guaranteed to list its
 * rows in the same order the saved one did. */
function conflictsByPath(report) {
  const map = new Map();
  for (const c of report?.conflicts ?? []) map.set(c.path, c);
  return map;
}

export function ReorderModal({ modal, state, actions }) {
  if (modal?.type !== "reorder") return null;

  const mods = state.mods?.mods ?? [];
  const modsByKey = useMemo(
    () => new Map(mods.map((m) => [modKey(m), m])),
    [mods],
  );

  const [order, setOrder] = useState(() => buildInitialOrder(mods));
  // Re-seed when the underlying library list's OWN identity changes (a
  // re-hydrate while the modal happens to be open) - not on MOUNT, which
  // the useState initializer above already handles. Preact/hooks flushes
  // effects asynchronously after the commit that mounted this component
  // (its own doc comment: "measured empirically at up to a handful of
  // frames in a headless browser"), so an unconditional effect here races
  // a fast first click: the mount effect can still be pending when a user
  // (or an E2E driver) reorders within that window, and firing it THEN
  // would silently overwrite the very edit that just happened with the
  // identical-looking but now-stale initial order. mountedMods tracks the
  // state.mods reference this modal has already seeded FROM, so the
  // effect only acts on a GENUINE change to it.
  const mountedMods = useRef(state.mods);
  useEffect(() => {
    if (mountedMods.current === state.mods) return;
    mountedMods.current = state.mods;
    setOrder(buildInitialOrder(mods));
  }, [state.mods]);

  // The library's own ⋯ menu offers "Reorder here" per row, and the
  // Conflicts card's own "Resolve…" does the same for the conflict's
  // deployed owner (demo item 9, unit 6 gate review) - modal.focusKey
  // carries which one, so opening the modal from a specific row or
  // conflict lands the user on it instead of the top of a possibly long
  // list. Scoped to THIS list, not the whole document - the library table
  // behind the modal stamps the identical data-mod on its own rows, so an
  // unscoped query silently found whichever the DOM happened to list
  // first (the library's, not the modal's - never actually this row).
  // tabindex="-1" (on the row below) makes the row a legitimate focus()
  // target without adding it to the page's own Tab order - it is a scroll
  // destination, not a control.
  useEffect(() => {
    if (!modal.focusKey) return;
    const row = document.querySelector(
      `[data-testid="reorder-list"] [data-mod="${CSS.escape(modal.focusKey)}"]`,
    );
    row?.scrollIntoView({ block: "center" });
    row?.focus();
  }, []);

  const [dragKey, setDragKey] = useState(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(null);
  const [preview, setPreview] = useState({
    status: "loading",
    report: null,
    error: null,
  });
  const previewSeq = useRef(0);

  // The debounced, seq-fenced conflict preview - main.js#omnibarSeq's
  // pattern, kept local to this modal (nothing else needs "the order this
  // modal currently proposes"). A slow response for an order the user has
  // since edited past is dropped rather than landing late.
  useEffect(() => {
    previewSeq.current += 1;
    const seq = previewSeq.current;
    const context = { game: state.route.game, profile: modal.profileName };
    const timer = setTimeout(() => {
      setPreview((p) => ({ status: "loading", report: p.report, error: null }));
      conflictsForOrder(order, context).then(
        (report) => {
          if (previewSeq.current !== seq) return;
          setPreview({ status: "ready", report, error: null });
        },
        (err) => {
          if (previewSeq.current !== seq) return;
          setPreview({
            status: "error",
            report: null,
            error: err?.message ?? String(err),
          });
        },
      );
    }, PREVIEW_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [order.join(","), modal.profileName]);

  function close() {
    if (saving) return;
    actions.closeModal();
  }

  function move(key, delta) {
    setOrder((prev) => {
      const i = prev.indexOf(key);
      const j = i + delta;
      if (i < 0 || j < 0 || j >= prev.length) return prev;
      const next = prev.slice();
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });
  }

  function moveToEdge(key, edge) {
    setOrder((prev) => {
      const i = prev.indexOf(key);
      if (i < 0) return prev;
      const next = prev.slice();
      next.splice(i, 1);
      if (edge === "first") next.unshift(key);
      else next.push(key);
      return next;
    });
  }

  // Pointer drag: PLAIN mouse events, not the HTML5 Drag and Drop API.
  // Native drag-and-drop's dragstart/dragover events are only ever raised
  // by the browser's own gesture recognizer, which a synthetic
  // mousedown/mousemove/mouseup sequence (what chromedp's E2E drive - and
  // any other input-automation tool - actually dispatches) never triggers;
  // building this on mousedown/mouseenter/mouseup instead means the exact
  // same events a real drag produces are what drive it, so nothing here is
  // untestable by construction.
  function onRowMouseDown(key) {
    if (saving) return;
    setDragKey(key);
  }
  function onRowMouseEnter(key) {
    if (!dragKey || dragKey === key) return;
    setOrder((prev) => {
      const from = prev.indexOf(dragKey);
      const to = prev.indexOf(key);
      if (from < 0 || to < 0) return prev;
      const next = prev.slice();
      next.splice(from, 1);
      next.splice(to, 0, dragKey);
      return next;
    });
  }
  useEffect(() => {
    if (!dragKey) return;
    function stop() {
      setDragKey(null);
    }
    window.addEventListener("mouseup", stop);
    return () => window.removeEventListener("mouseup", stop);
  }, [dragKey]);

  async function save() {
    setSaving(true);
    setSaveError(null);
    try {
      await reorderProfile(modal.profileName, order, {
        game: state.route.game,
        profile: modal.profileName,
      });
      await Promise.all([actions.reloadMods(), actions.reloadConflicts()]);
      actions.closeModal();
    } catch (err) {
      setSaveError(err?.message ?? String(err));
    } finally {
      setSaving(false);
    }
  }

  const currentByPath = conflictsByPath(state.conflicts);
  const proposedByPath = conflictsByPath(preview.report);
  const paths = [
    ...new Set([...currentByPath.keys(), ...proposedByPath.keys()]),
  ];

  return html`
    <${Modal}
      kind="reorder"
      title="Reorder load order"
      onClose=${saving ? () => {} : close}
      footer=${html`
        <button
          type="button"
          class="button"
          disabled=${saving}
          onClick=${close}
        >
          Cancel
        </button>
        <button
          type="button"
          class="button button--primary"
          data-action="save-order"
          disabled=${saving}
          onClick=${save}
        >
          ${saving ? "Saving…" : "Save order"}
        </button>
      `}
    >
      <p class="plan__note">
        Order below is lowest priority first: a mod further down this list
        wins a file conflict against one above it.
      </p>
      ${saveError && html`<p class="modal__error">${saveError}</p>`}

      <ul class="reorder-list" data-testid="reorder-list">
        ${order.map((key, i) => {
          const mod = modsByKey.get(key);
          if (!mod) return null;
          return html`
            <li
              key=${key}
              class="reorder-row ${dragKey === key ? "reorder-row--dragging" : ""}"
              data-mod=${key}
              tabindex="-1"
              onMouseEnter=${() => onRowMouseEnter(key)}
            >
              <span
                class="reorder-row__handle"
                aria-label=${`Drag to reorder ${mod.name}`}
                onMouseDown=${() => onRowMouseDown(key)}
              >
                ⠿
              </span>
              <span class="reorder-row__position mono">${i + 1}</span>
              <span class="reorder-row__name">${mod.name}</span>
              <span class="reorder-row__controls">
                <button
                  type="button"
                  class="button button--small"
                  aria-label=${`Move ${mod.name} up`}
                  disabled=${saving || i === 0}
                  onClick=${() => move(key, -1)}
                >
                  ↑
                </button>
                <button
                  type="button"
                  class="button button--small"
                  aria-label=${`Move ${mod.name} down`}
                  disabled=${saving || i === order.length - 1}
                  onClick=${() => move(key, 1)}
                >
                  ↓
                </button>
                <button
                  type="button"
                  class="button button--small"
                  aria-label=${`Move ${mod.name} to lowest priority`}
                  disabled=${saving || i === 0}
                  onClick=${() => moveToEdge(key, "first")}
                >
                  First
                </button>
                <button
                  type="button"
                  class="button button--small"
                  aria-label=${`Move ${mod.name} to highest priority`}
                  disabled=${saving || i === order.length - 1}
                  onClick=${() => moveToEdge(key, "last")}
                >
                  Last
                </button>
              </span>
            </li>
          `;
        })}
      </ul>

      <section class="plan__section">
        <h3 class="plan__heading">
          Conflict preview
          ${preview.status === "loading" ? " — computing…" : ""}
        </h3>
        ${
          preview.status === "error"
            ? html`<p class="modal__error">
                Couldn't preview this order: ${preview.error}
              </p>`
            : paths.length === 0
              ? html`<p class="plan__note">
                  No file conflicts in this profile.
                </p>`
              : html`
                  <table class="reorder-preview">
                    <thead>
                      <tr>
                        <th>Path</th>
                        <th>Current winner</th>
                        <th>Proposed winner</th>
                      </tr>
                    </thead>
                    <tbody>
                      ${paths.map((path) => {
                        const current = currentByPath.get(path);
                        const proposed = proposedByPath.get(path);
                        const changed =
                          current &&
                          proposed &&
                          current.load_order_winner.key !==
                            proposed.load_order_winner.key;
                        return html`
                          <tr key=${path}>
                            <td class="mono">${path}</td>
                            <td>
                              ${current?.load_order_winner.name ?? "—"}
                              ${
                                current?.stale &&
                                html`<span
                                  class="badge badge--warn"
                                  title=${`A redeploy would change which file wins (${staleWinnerNote})`}
                                  >redeploy needed</span
                                >`
                              }
                            </td>
                            <td
                              class=${changed ? "reorder-preview__changed" : ""}
                            >
                              ${proposed?.load_order_winner.name ?? "—"}
                            </td>
                          </tr>
                        `;
                      })}
                    </tbody>
                  </table>
                `
        }
      </section>
    </${Modal}>
  `;
}
