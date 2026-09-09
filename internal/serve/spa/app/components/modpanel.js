// modpanel.js - the ?mod= slide-over, the default drill-in
// (docs/plans/2026-08-31-serve-spa-design.md §Slide-over): name, author,
// installed -> available version, editable lock + update-policy controls,
// summary prose, key actions (update/enable/disable/uninstall), this mod's
// own findings/conflicts, a changelog preview, and "More info ->" onto the
// full mod page. Esc / outside click closes; <-/-> step through the
// current (filtered/sorted) list Library is showing.
//
// Everything above the changelog preview renders from documents Mission
// Control ALREADY fetched (rows/health/conflicts - all passed down from
// missioncontrol.js) - zero extra requests, preserving Unit 2's own "opening
// the slide-over must not rehydrate" guarantee. The changelog is the one
// exception: it is not persisted anywhere (core.ModDetail's own doc
// comment - a live source.ChangelogProvider read every time), so this is
// the one section that fetches, lazily, once the panel is open.

import { html, useEffect, useMemo, useRef, useState } from "../render.js";
import { trapFocus } from "../focustrap.js";
import { exitMillis } from "../motion.js";
import { navigate } from "../router.js";
import { getModDetail, ApiError } from "../api.js";
import { findingLabel } from "../verify.js";
import { InlineJob } from "./jobprogress.js";

/** modUrl builds the ?mod= URL for row, on the given base path - the same
 * annotation library.js#openRow writes, reused here for </-> stepping. */
function modUrl(basePath, row) {
  const url = new URL(basePath, window.location.origin);
  url.searchParams.set("mod", `${row.source_id}/${row.id}`);
  return url.pathname + url.search;
}

/** findingsFor returns modID's own VerifyFinding rows, excluding "ok".
 * Shared with the full mod page since I-5 (epic live review). */
export function findingsFor(health, modID) {
  return (health?.result?.findings ?? []).filter(
    (f) => f.mod_id === modID && f.status !== "ok",
  );
}

/** conflictsFor returns the ProfileConflict rows that name row's key, either
 * as the current owner or as one of the other providers. Shared with the
 * full mod page since I-5 (epic live review). */
export function conflictsFor(conflicts, key) {
  return (conflicts?.conflicts ?? []).filter(
    (c) => c.owner.key === key || c.also_in.some((m) => m.key === key),
  );
}

export function ModPanel({
  modKey,
  contextPath,
  rows,
  visible,
  catalogRows,
  route,
  state,
  actions,
}) {
  const [sourceID, modID] = (modKey ?? "").split("/", 2);
  const row = useMemo(
    () => (rows ?? []).find((r) => r.source_id === sourceID && r.id === modID),
    [rows, sourceID, modID],
  );
  // catalogMod (issue 331) is the omnibar fan-out's own answer for the SAME
  // key, checked only when the mod isn't installed (row is undefined): a
  // search result opens this same slide-over (design doc §Search: "slide-
  // over on click for source results too"), just with nothing installed to
  // show - name/version/author/summary plus Install, not the full
  // lock/policy/findings/changelog surface an installed row gets.
  const catalogMod = useMemo(
    () =>
      row
        ? null
        : (catalogRows ?? []).find(
            (m) => m.source_id === sourceID && m.id === modID,
          ),
    [row, catalogRows, sourceID, modID],
  );

  const index = useMemo(
    () =>
      (visible ?? []).findIndex(
        (r) => r.source_id === sourceID && r.id === modID,
      ),
    [visible, sourceID, modID],
  );
  const prevRow = index > 0 ? visible[index - 1] : null;
  const nextRow =
    index >= 0 && index < (visible?.length ?? 0) - 1
      ? visible[index + 1]
      : null;

  // The exit animation (issue 334, owner demo 1: "mild UI animations for the
  // slide-over"). Preact would unmount this whole subtree the instant the
  // route lost its ?mod=, so there would be nothing left on screen to
  // animate - the panel would simply vanish. Holding the ROUTE CHANGE back
  // for one --motion-base, with a class on the scrim that plays the closing
  // keyframes, is what gives the exit somewhere to happen.
  //
  // Every one of this panel's four exits (Escape, the scrim, the ✕, the
  // ←/→ that leave it) runs through close(), so one deferral covers them
  // all. `closing` also latches: a second Escape during the animation must
  // not queue a second navigate.
  const [closing, setClosing] = useState(false);

  function close() {
    if (closing) return;
    if (exitMillis() === 0) {
      navigate(contextPath);
      return;
    }
    setClosing(true);
    setTimeout(() => navigate(contextPath), exitMillis());
  }

  const panelRef = useRef(null);
  const closeRef = useRef(close);
  closeRef.current = close;
  const stepRef = useRef({ prevRow, nextRow });
  stepRef.current = { prevRow, nextRow };

  // Esc closes, </-> step through `visible` - a single document-level
  // keydown listener (refs, not the raw values, so this effect never has
  // to re-attach on every render). Outside click is handled separately,
  // below, as a plain onClick on the scrim: matching Modal's own pattern
  // (components/modal.js) exactly, which is also what makes it reliable
  // under chromedp - a document-level "pointerdown" listener (this file's
  // first cut) never observed chromedp's synthetic
  // Input.dispatchMouseEvent clicks at all, which in headless Chrome does
  // not synthesize a pointerdown the way a real click does.
  useEffect(() => {
    function handleKeyDown(e) {
      if (e.key === "Escape") {
        closeRef.current();
        return;
      }
      // Arrow keys are ignored while the focus is inside a form control
      // (the lock/policy selects below) - otherwise pressing Left/Right to
      // move a cursor in a text field, or to open a <select>, would
      // instead navigate the whole panel away from under the user.
      const tag = document.activeElement?.tagName;
      if (tag === "SELECT" || tag === "INPUT" || tag === "TEXTAREA") return;
      if (e.key === "ArrowLeft" && stepRef.current.prevRow) {
        navigate(modUrl(contextPath, stepRef.current.prevRow));
      } else if (e.key === "ArrowRight" && stepRef.current.nextRow) {
        navigate(modUrl(contextPath, stepRef.current.nextRow));
      }
    }
    document.addEventListener("keydown", handleKeyDown);
    // Focus the panel on open, matching Modal's own reasoning: without it,
    // keyboard focus stays on the row button that opened this panel - now
    // behind (or beside) it - and Tab starts walking the page underneath.
    // Also confirmed to matter for chromedp's own synthetic key dispatch:
    // Input.dispatchKeyEvent with nothing in the page focused does not
    // reliably reach a bare document-level listener the way a real
    // keypress does once something holds focus.
    panelRef.current?.focus();
    // Keyboard containment, matching the modal's (issue 334's a11y pass):
    // this panel has a scrim over the page too, and nothing but this told
    // the Tab key so.
    const releaseTrap = trapFocus(() => panelRef.current);
    return () => {
      releaseTrap();
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [contextPath]);

  /** closeOnScrim closes only when the click landed on the scrim ITSELF,
   * not a bubbled click from inside the panel - Modal's own
   * e.target === e.currentTarget check. */
  function closeOnScrim(e) {
    if (e.target === e.currentTarget) close();
  }

  // The changelog preview's own lazy fetch - see this file's header
  // comment for why it, alone, cannot render from what Mission Control
  // already has on hand.
  const [changelog, setChangelog] = useState({
    status: "loading",
    text: "",
    error: "",
  });
  useEffect(() => {
    if (!sourceID || !modID) return;
    let cancelled = false;
    setChangelog({ status: "loading", text: "", error: "" });
    getModDetail(sourceID, modID, { game: route.game, profile: route.profile })
      .then((detail) => {
        if (cancelled) return;
        setChangelog({
          status: "ready",
          text: detail.changelog ?? "",
          error: "",
        });
      })
      .catch((err) => {
        if (cancelled) return;
        setChangelog({
          status: "error",
          text: "",
          error: err instanceof ApiError ? err.message : String(err),
        });
      });
    return () => {
      cancelled = true;
    };
  }, [sourceID, modID, route.game, route.profile]);

  if (!row && catalogMod) {
    const origin = `install:${catalogMod.source_id}/${catalogMod.id}`;
    return html`
      <div
        class="slide-over ${closing ? "slide-over--closing" : ""}"
        role="dialog"
        aria-modal="true"
        aria-label="${catalogMod.name} details"
        onClick=${closeOnScrim}
      >
        <div class="slide-over__panel" ref=${panelRef} tabindex="-1">
          <button
            type="button"
            class="slide-over__close"
            onClick=${close}
            aria-label="Close"
          >
            ×
          </button>
          <h2 class="section-header">${catalogMod.name}</h2>
          <p class="slide-over__meta">
            ${catalogMod.author ? html`by ${catalogMod.author} · ` : ""}
            <span class="mono">${catalogMod.version}</span> ·
            <span class="badge">${catalogMod.source_id}</span>
          </p>
          ${
            catalogMod.summary &&
            html`<p class="slide-over__summary">${catalogMod.summary}</p>`
          }
          <div class="slide-over__actions">
            ${
              catalogMod.installed
                ? html`<span class="badge badge--good">Installed</span>`
                : html`<${InlineJob}
                    origin=${origin}
                    state=${state}
                    actions=${actions}
                  >
                    <button
                      type="button"
                      class="button button--primary"
                      onClick=${() =>
                        actions.openPlan({
                          kind: "install",
                          origin,
                          title: `Install ${catalogMod.name}`,
                          confirmLabel: "Install",
                          options: {
                            source_id: catalogMod.source_id,
                            mod_id: catalogMod.id,
                          },
                        })}
                    >
                      Install
                    </button>
                  <//>`
            }
          </div>
          <section
            class="slide-over__section"
            data-changelog-status=${changelog.status}
          >
            <h3 class="plan__heading">Changelog</h3>
            ${
              changelog.status === "loading"
                ? html`<p class="empty-state__hint">Loading changelog…</p>`
                : changelog.status === "error"
                  ? html`<p class="empty-state__hint">
                      Couldn't load the changelog: ${changelog.error}
                    </p>`
                  : changelog.text
                    ? html`<p class="slide-over__changelog">
                        ${changelogPreview(changelog.text)}
                      </p>`
                    : html`<p class="empty-state__hint">
                        No changelog available.
                      </p>`
            }
          </section>
        </div>
      </div>
    `;
  }

  if (!row) {
    return html`
      <div
        class="slide-over ${closing ? "slide-over--closing" : ""}"
        role="dialog"
        aria-modal="true"
        aria-label="Mod details"
        onClick=${closeOnScrim}
      >
        <div class="slide-over__panel" ref=${panelRef} tabindex="-1">
          <button
            type="button"
            class="slide-over__close"
            onClick=${close}
            aria-label="Close"
          >
            ×
          </button>
          <h2 class="section-header">Mod details</h2>
          <p class="empty-state__hint">
            <span class="mono">${sourceID} / ${modID}</span> is not in this
            profile's library.
          </p>
        </div>
      </div>
    `;
  }

  const findings = findingsFor(state.health, row.id);
  const conflicts = conflictsFor(state.conflicts, row.key);
  const origin = (action) => `mod:${row.source_id}/${row.id}:${action}`;

  return html`
    <div
      class="slide-over ${closing ? "slide-over--closing" : ""}"
      role="dialog"
      aria-modal="true"
      aria-label="${row.name} details"
      onClick=${closeOnScrim}
    >
      <div class="slide-over__panel" ref=${panelRef} tabindex="-1">
        <div class="slide-over__nav">
          <button
            type="button"
            class="slide-over__step"
            disabled=${!prevRow}
            aria-label="Previous mod"
            onClick=${() => prevRow && navigate(modUrl(contextPath, prevRow))}
          >
            ←
          </button>
          <button
            type="button"
            class="slide-over__step"
            disabled=${!nextRow}
            aria-label="Next mod"
            onClick=${() => nextRow && navigate(modUrl(contextPath, nextRow))}
          >
            →
          </button>
          <button
            type="button"
            class="slide-over__close"
            onClick=${close}
            aria-label="Close"
          >
            ×
          </button>
        </div>

        <h2 class="section-header">${row.name}</h2>
        <p class="slide-over__meta">
          ${row.author ? html`by ${row.author} · ` : ""}
          <span class="mono"
            >${row.version}${row.hasUpdate && html` → ${row.updateTarget}`}</span
          >
        </p>

        ${row.external && html`<${ManagedBySteam} row=${row} />`}

        <${ModSettingsControls}
          row=${row}
          actions=${actions}
          panelRef=${panelRef}
        />

        ${
          row.summary && html`<p class="slide-over__summary">${row.summary}</p>`
        }

        <div class="slide-over__actions">
          ${
            row.hasUpdate &&
            !row.external &&
            html`<${InlineJob}
              origin=${origin("update")}
              state=${state}
              actions=${actions}
            >
              <button
                type="button"
                class="button button--primary"
                onClick=${() =>
                  actions.openPlan({
                    kind: "updates",
                    origin: origin("update"),
                    title: `Update ${row.name}`,
                    confirmLabel: "Update",
                    options: { mods: [`${row.source_id}:${row.id}`] },
                  })}
              >
                Update
              </button>
            <//>`
          }
          ${
            !row.external &&
            html`<${InlineJob}
              origin=${origin("toggle")}
              state=${state}
              actions=${actions}
            >
              <button
                type="button"
                class="button"
                onClick=${() =>
                  actions.startToggle({
                    action: row.enabled ? "disable" : "enable",
                    sourceID: row.source_id,
                    modID: row.id,
                    origin: origin("toggle"),
                  })}
              >
                ${row.enabled ? "Disable" : "Enable"}
              </button>
            <//>`
          }
          <${InlineJob}
            origin=${origin("uninstall")}
            state=${state}
            actions=${actions}
          >
            <button
              type="button"
              class="button button--danger"
              onClick=${() =>
                actions.openPlan({
                  kind: "uninstall",
                  origin: origin("uninstall"),
                  title: row.external
                    ? `Stop tracking ${row.name}`
                    : `Uninstall ${row.name}`,
                  confirmLabel: row.external ? "Stop tracking" : "Uninstall",
                  options: { source_id: row.source_id, mod_id: row.id },
                })}
            >
              ${row.external ? "Stop tracking" : "Uninstall"}
            </button>
          <//>
        </div>

        ${
          findings.length > 0 &&
          html`
            <section class="slide-over__section">
              <h3 class="plan__heading">Findings (${findings.length})</h3>
              <ul class="plan__paths">
                ${
                  // M1, unit 8 gate review: this printed the raw status slug
                  // ("version_mismatch", "no_checksum") while the Health card
                  // two inches to its left rendered the same finding as
                  // prose. findingLabel is the shared answer verify.js's own
                  // doc comment says exists "so the two surfaces can never
                  // drift" - the slide-over is the third surface that never
                  // adopted it.
                  findings.map(
                    (f) =>
                      html`<li key=${f.file_id ?? f.status}>
                        ${findingLabel(f)}
                      </li>`,
                  )
                }
              </ul>
            </section>
          `
        }
        ${
          conflicts.length > 0 &&
          html`
            <section class="slide-over__section">
              <h3 class="plan__heading">Conflicts (${conflicts.length})</h3>
              <ul class="plan__paths">
                ${conflicts.map(
                  (c) =>
                    html`<li key=${c.path} class="mono">
                      ${c.path}
                      ${
                        c.load_order_winner.key === row.key
                          ? " (wins)"
                          : ` (loses to ${c.load_order_winner.name})`
                      }
                    </li>`,
                )}
              </ul>
            </section>
          `
        }

        <section
          class="slide-over__section"
          data-changelog-status=${changelog.status}
        >
          <h3 class="plan__heading">Changelog</h3>
          ${
            changelog.status === "loading"
              ? html`<p class="empty-state__hint">Loading changelog…</p>`
              : changelog.status === "error"
                ? html`<p class="empty-state__hint">
                    Couldn't load the changelog: ${changelog.error}
                  </p>`
                : changelog.text
                  ? html`<p class="slide-over__changelog">
                      ${changelogPreview(changelog.text)}
                    </p>`
                  : html`<p class="empty-state__hint">
                      No changelog available.
                    </p>`
          }
        </section>

        <a
          class="slide-over__more"
          href="${contextPath}/mod/${encodeURIComponent(row.source_id)}/${encodeURIComponent(row.id)}"
          onClick=${(e) => {
            e.preventDefault();
            navigate(
              `${contextPath}/mod/${encodeURIComponent(row.source_id)}/${encodeURIComponent(row.id)}`,
            );
          }}
        >
          More info →
        </a>
      </div>
    </div>
  `;
}

/** changelogPreview keeps the slide-over's own copy short - the FULL text
 * is the full mod page's job (design doc: "complete changelog"), this is
 * only a preview. */
function changelogPreview(text) {
  const trimmed = text.trim();
  const limit = 400;
  return trimmed.length > limit ? `${trimmed.slice(0, limit)}…` : trimmed;
}

/** ModSettingsControls is the editable lock + update-policy pair, over the
 * thin api_mod_settings.go routes (no plan, no job - a single DB write with
 * nothing to preview). Local status/error state only: a successful write
 * lets refreshAfterModSetting (main.js) bring fresh data back down through
 * `row` on the next render, so this component never has to hold its own
 * copy of what changed.
 *
 * Shared with the full mod page since I-5 (epic live review): "More info →"
 * used to lead to a surface with FEWER actions than the panel it came from,
 * on a page whose own design section opens with "Everything, unlimited
 * room". panelRef is optional - it exists for the slide-over's focus rule
 * (below), and the full mod page has no panel to return focus TO. */
export function ModSettingsControls({ row, actions, panelRef }) {
  const [state, setState] = useState({ busy: false, error: "" });

  // M4: the checkbox/select's own `disabled` attribute (set below, for the
  // whole write) blurs it the instant the browser applies it - a disabled
  // control cannot hold focus - and focus never returns to the panel on
  // its own once the write settles and re-enables it. Restoring it here,
  // once either write settles, is what keeps waitForPanelFocus() (and Esc/
  // the arrow steps, which rely on the panel - not some stray control -
  // holding focus) usable after a settings interaction.
  function restorePanelFocus() {
    // panelRef?, not panelRef.current? - the full mod page (I-5) renders
    // these controls with no panel at all, and reading .current off an
    // absent ref is a TypeError, not a no-op.
    panelRef?.current?.focus();
  }

  async function toggleLock() {
    setState({ busy: true, error: "" });
    try {
      if (row.locked) {
        await actions.clearModLock(row.source_id, row.id);
      } else {
        await actions.setModLock(row.source_id, row.id, "");
      }
      setState({ busy: false, error: "" });
    } catch (err) {
      setState({
        busy: false,
        error: err instanceof ApiError ? err.message : String(err),
      });
    } finally {
      restorePanelFocus();
    }
  }

  // convert_paks is a TRI-STATE on the wire (core.ModListing): null means
  // pak conversion does not apply to this mod at all - not a merge-compile
  // game, or no pak merge source - which is distinct from a non-null false
  // meaning "applies, and is off". So the control renders only when the
  // wire says the question is even askable (C-3).
  const convertApplies =
    row.convert_paks !== null && row.convert_paks !== undefined;

  async function toggleConvert() {
    setState({ busy: true, error: "" });
    try {
      await actions.setModConvert(row.source_id, row.id, !row.convert_paks);
      setState({ busy: false, error: "" });
    } catch (err) {
      setState({
        busy: false,
        error: err instanceof ApiError ? err.message : String(err),
      });
    } finally {
      restorePanelFocus();
    }
  }

  async function changePolicy(policy) {
    setState({ busy: true, error: "" });
    try {
      await actions.setModUpdatePolicy(row.source_id, row.id, policy);
      setState({ busy: false, error: "" });
    } catch (err) {
      setState({
        busy: false,
        error: err instanceof ApiError ? err.message : String(err),
      });
    } finally {
      restorePanelFocus();
    }
  }

  return html`
    <div class="slide-over__settings">
      <label class="slide-over__setting">
        <input
          type="checkbox"
          checked=${row.locked}
          disabled=${state.busy}
          onChange=${toggleLock}
        />
        Locked${row.locked && row.locked_version ? html` at <span class="mono">${row.locked_version}</span>` : ""}
      </label>
      <label class="slide-over__setting">
        Update policy
        <select
          value=${row.update_policy}
          disabled=${state.busy}
          onChange=${(e) => changePolicy(e.currentTarget.value)}
        >
          <option value="notify">Notify</option>
          <option value="auto">Auto</option>
          <option value="pinned">Pinned</option>
        </select>
      </label>
      ${
        convertApplies &&
        html`<label class="slide-over__setting">
          <input
            type="checkbox"
            name="convert-paks"
            checked=${row.convert_paks}
            disabled=${state.busy}
            onChange=${toggleConvert}
          />
          Convert paks
        </label>`
      }
      ${state.error && html`<p class="empty-state__hint">${state.error}</p>`}
    </div>
  `;
}

/**
 * ManagedBySteam is the "this mod is not lmm's to manage" block (issue 269).
 *
 * It states the fact once, in one place, for both the slide-over and the
 * full mod page: Steam owns the item's files where they sit, lmm tracks it
 * and reports its updates, and the actions that would imply otherwise
 * (deploy, enable/disable, update, rollback, relink) are not shown at all
 * rather than shown-and-refused. Uninstall stays, reworded, because
 * removing lmm's tracking IS something the user can do here.
 */
const STEAM_OWNERSHIP_NOTE =
  "lmm tracks this Steam Workshop item and checks it for updates. " +
  "Steam owns its files and applies its updates the next time you launch " +
  "the game. Uninstalling it here removes lmm's tracking only - unsubscribe " +
  "in the Steam client to remove the item itself.";

export function ManagedBySteam({ row }) {
  return html`
    <section class="slide-over__section" data-testid="managed-by-steam">
      <h3 class="slide-over__heading">Managed by Steam</h3>
      <p class="slide-over__detail mono">${row.external_path}</p>
      <p class="slide-over__detail">${STEAM_OWNERSHIP_NOTE}</p>
      ${
        row.version &&
        html`<p class="slide-over__detail mono">
          Steam content id: ${row.version}
        </p>`
      }
    </section>
  `;
}
