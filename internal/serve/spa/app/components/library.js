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

import { html, useEffect, useMemo, useRef, useState } from "../render.js";
import { navigate } from "../router.js";
import { ApiError } from "../api.js";
import {
  formatDate,
  countExternal,
  countOf,
  healthBadge,
  healthLabel,
  FILTER_NAMES,
  SORT_NAMES,
} from "../modrows.js";
import { mutationLabel, progressText } from "../progress.js";
import { pendingToggleLabel, toggleRequestFor } from "../toggleack.js";
import { displayVersion } from "../version.js";
import { AddModsMenu } from "./addmodsmenu.js";
import { ListedOffList, listedOffRefs } from "./listedoff.js";
import { InlineJob } from "./jobprogress.js";

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

// UPDATE_ALL_ORIGIN is the library header's own "Update all" (issue 417) -
// distinct from the batch bar's "library:batch-update", which acts on a
// selection, and from the Updates card's own control. Three controls that
// can plan different sets must not share one origin, or one of them morphs
// into a job it did not start.
const UPDATE_ALL_ORIGIN = "library:update-all";

// healthBadgeTone maps issue 418's three health states onto the badge
// classes that already exist. "unknown" takes the plain badge deliberately:
// not having looked yet is not a warning, and colouring it as one would
// make a fresh page load read as a problem.
function healthBadgeTone(row) {
  if (row.healthState === "issues") return "badge--warn";
  if (row.healthState === "ok") return "badge--good";
  return "";
}

// nonTextInputTypes are the <input> types that take no typing: a keystroke
// aimed at one of these is a command, not a character.
const nonTextInputTypes = new Set([
  "checkbox",
  "radio",
  "button",
  "submit",
  "reset",
  "range",
  "color",
  "file",
]);

/** isTypingTarget reports whether a keystroke aimed at el is somebody
 * TYPING - in which case a single-letter binding must keep its hands off it
 * (issue 434's "a").
 *
 * Finer-grained than app.js's own list for the `?` binding, and it has to
 * be: the control that most often holds focus when this binding is pressed
 * is the select-all CHECKBOX the user just clicked, which is an <input> and
 * takes no text at all. A <select> counts as typing even though it does
 * not: a letter pressed on a focused one jumps to the option starting with
 * it, which is a browser behaviour worth more than a shortcut. */
function isTypingTarget(el) {
  if (!(el instanceof HTMLElement)) return false;
  if (el.isContentEditable) return true;
  if (el.tagName === "TEXTAREA" || el.tagName === "SELECT") return true;
  if (el.tagName !== "INPUT") return false;
  return !nonTextInputTypes.has((el.type || "text").toLowerCase());
}

export function Library({
  state,
  mods,
  rows,
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

  // issue 432: the enable/disable a row's user asked for and has not yet
  // seen settled (toggleack.js), or undefined.
  const requestedFor = (row) => toggleRequestFor(state, row.key);

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

  // issue 434: the rows select-all actually takes. "Everything currently
  // visible" is the filter's and the omnibar's answer, not the library's -
  // the same `visible` selectedRows() already measures against - minus the
  // rows no batch action can act on. An EXTERNAL row is the one such case
  // today: core refuses enable/disable for a Steam Workshop item outright
  // (issue 379, which is why the row's own enabled checkbox is disabled), so
  // sweeping it in would hand the batch bar a selection its two main
  // buttons then have to refuse. Its own checkbox stays live - a user who
  // means that row can still tick it - it is only never taken in bulk.
  function selectableRows() {
    return visible.filter((r) => !r.isExternal);
  }

  const selectable = selectableRows();
  const takenCount = selectable.filter((r) => selected.has(r.key)).length;
  const allTaken = selectable.length > 0 && takenCount === selectable.length;

  function toggleSelectAll() {
    setSelected((prev) => {
      const next = new Set(prev);
      if (allTaken) {
        // Clears what is IN VIEW, not the whole set: a row selected under a
        // different filter is not something this press was about.
        for (const row of visible) next.delete(row.key);
        return next;
      }
      for (const row of selectable) next.add(row.key);
      return next;
    });
  }

  // "a" selects everything in view, or clears it (issue 434, shortcuts.js).
  // Scoped to this component rather than app.js's own global `?` handler
  // because it is scoped to this SURFACE: the library is the only screen
  // with a selection to take.
  //
  // Ignored while a text field has focus - "a" is a character someone is
  // entitled to type into the omnibar - and while anything is layered over
  // the table: a modal, for the reason app.js states (modals stack at most
  // one deep, and reaching past one to change the page underneath is not
  // something a keystroke should do), and the slide-over, which is a route
  // annotation rather than a modal but covers the table just the same. A
  // selection silently changing behind a panel you are reading is not a
  // shortcut, it is a surprise.
  //
  // The listener is attached ONCE and reads the current handler through a
  // ref: this component re-renders on every activity frame while a job runs,
  // and a listener swapped on each of those would be churn for nothing -
  // while a handler captured on the first render would select against a
  // `visible` that has since changed.
  const coveredByOverlay = Boolean(state.modal) || Boolean(state.route?.mod);
  const selectAllRef = useRef(null);
  selectAllRef.current = coveredByOverlay ? null : toggleSelectAll;
  useEffect(() => {
    function onKeyDown(e) {
      if (e.key !== "a" || e.ctrlKey || e.metaKey || e.altKey) return;
      if (!selectAllRef.current) return;
      if (isTypingTarget(e.target)) return;
      e.preventDefault();
      selectAllRef.current();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

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

  // The reason the two toggle buttons are refused, or nothing (P2 review
  // Nit 8).
  //
  // selectedRows() filters against `visible`, so a filter or a search that
  // hides every selected row leaves selected.size > 0 - the bar renders -
  // while togglableSelectedRows() is empty. Naming Steam there states a
  // reason the bar cannot know: the selection may hold no Steam row at all,
  // and what is actually true is that nothing it could act on is in view.
  // The button stays refused either way; only the sentence is withheld.
  //
  // The two reasons a row in view is left out are each named when they are
  // the whole story, and together when both are.
  function toggleRefusalTitle(action) {
    if (togglableSelectedRows().length > 0) return undefined;
    const rows = selectedRows();
    if (rows.length === 0) return undefined;
    if (rows.every((r) => r.isExternal))
      return `Steam manages the selected items — lmm cannot ${action} them`;
    if (rows.every((r) => requestedFor(r) !== undefined))
      return "The selected mods are already being enabled or disabled";
    return `The selected mods are managed by Steam or already being enabled or disabled — lmm cannot ${action} them now`;
  }

  // issue 432: the click is acknowledged by toggleack.js in the same frame -
  // the box moves to what was asked for and the row says what it is doing -
  // rather than sitting on the old value, greyed, until the deploy behind it
  // finishes. Nothing is awaited here: the pending state IS the feedback,
  // and the outcome lands through the row's own live line.
  function toggleEnabled(row) {
    actions.startToggle(row);
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

  // issue 417: the library header's own update pair. Everything they drive
  // already existed - the `updates` plan kind, and GET /api/v1/updates - and
  // nothing here is a new flow; what was missing is that neither read as THE
  // update action. The card only appears when there is already something to
  // report, the batch bar only appears once rows are ticked, and the ⋯ menu
  // hid the per-mod one behind a click.
  //
  // `rows`, not `visible`: "all" means every mod in this profile with an
  // update, not whatever a filter happens to be showing. The count on the
  // button says how many that is before the confirm modal does.
  function updatableRows() {
    // issue 269: an EXTERNAL row is excluded even when it HAS an update -
    // ApplyUpdateBatch declines it (core.ReasonExternalNoUpdate) - for the
    // same reason updatableSelectedRows drops one.
    return (rows ?? []).filter((r) => r.hasUpdate && !r.isExternal);
  }

  function updateAll() {
    const targets = updatableRows();
    if (targets.length === 0) return;
    actions.openPlan({
      kind: "updates",
      origin: UPDATE_ALL_ORIGIN,
      title: `Update ${countOf(targets.length, "mod")}`,
      confirmLabel: "Update",
      options: { mods: targets.map((r) => r.key) },
    });
  }

  // checking/verifying are these controls' own acknowledgment (the lesson of
  // issue 432, applied where it is needed next): an update check is a live
  // source read per mod and a full verify walks every deployed file, so a
  // button that did nothing visible until the whole thing came back would
  // read as a button that did nothing.
  const [checking, setChecking] = useState(false);
  const [verifying, setVerifying] = useState(false);

  async function checkForUpdates() {
    setChecking(true);
    try {
      await actions.refreshUpdates();
    } finally {
      setChecking(false);
    }
  }

  // issue 418: the library header's Verify. It is the SAME whole-profile
  // check the Health card's own button runs (actions.reloadHealth sends
  // ?force=1, opting out of core's unchanged-installation memo) - the
  // difference is that this one is on screen even when there is nothing
  // wrong, which is precisely when "is my install OK?" has no other answer:
  // an attention card renders only when it has something to say, so a
  // healthy profile had no verify control anywhere.
  async function verifyAll() {
    setVerifying(true);
    try {
      await actions.reloadHealth();
    } finally {
      setVerifying(false);
    }
  }

  function updateRow(row) {
    actions.openPlan({
      kind: "updates",
      origin: modOrigin(row, "update"),
      title: `Update ${row.name}`,
      confirmLabel: "Update",
      options: { mods: [row.key] },
    });
  }

  // issue 379: an EXTERNAL row is dropped for the same reason
  // updatableSelectedRows drops one, and the reason the row's own enabled
  // checkbox is disabled - core refuses enable/disable for a Steam
  // Workshop item, and no choice in this UI changes that. The batch bar
  // reached the identical doomed job in two clicks where the checkbox
  // reached it in one.
  //
  // issue 432: so is a row with a request already in flight. The ledger
  // holds one live request per mod; a batch that took the row again would
  // queue a second job behind the first, and whichever ran last would
  // silently decide the outcome.
  function togglableSelectedRows() {
    return selectedRows().filter(
      (r) => !r.isExternal && requestedFor(r) === undefined,
    );
  }

  function batchEnable(action) {
    const rows = togglableSelectedRows();
    if (rows.length === 0) return;
    setSelected(new Set());
    // issue 432: every row in the batch acknowledges at once, on the click -
    // the batch itself runs strictly one job at a time (main.js#
    // startBatchToggle), so without the ledger the last row of a long
    // selection sat visually untouched for the whole run.
    actions.startBatchToggle(
      action,
      rows.map((r) => ({
        key: r.key,
        source_id: r.source_id,
        id: r.id,
        name: r.name,
      })),
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
          // issue 417: Update is no longer in here. A mod with an update
          // pending now carries a VISIBLE button in its own actions cell -
          // the owner's note was that nothing in this UI read as "the update
          // action", and an action a click away behind ⋯ is exactly that.
          // Keeping a second copy here would be two controls doing one thing
          // on the same row.
          ""
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
          // issue 418: the per-row half of "is this mod OK?". Verify is the
          // whole-profile check - lmm has no per-mod verify, and the title
          // says so rather than letting the menu imply one - but it is
          // offered from the row because the row is where the question gets
          // asked, and the row's own badge is what answers it.
          html`<button
            type="button"
            class="row-menu__item"
            data-action="row-verify"
            title="Re-checks every mod in this profile — lmm verifies a profile as a whole"
            onClick=${() => {
              setMenuKey(null);
              verifyAll();
            }}
          >
            Verify
          </button>`
        }
        ${
          // Repair, on the other hand, IS per-mod: verify_fix takes a
          // mod_filter, which is the same plan the Health card's own
          // per-finding Repair opens - and the same origin, so whichever
          // surface is on screen shows the job.
          row.hasHealthIssue &&
          html`<button
            type="button"
            class="row-menu__item"
            data-action="row-repair"
            onClick=${() => {
              setMenuKey(null);
              actions.openPlan({
                kind: "verify_fix",
                origin: `health:${row.id}:repair`,
                title: `Repair ${row.name}`,
                confirmLabel: "Repair",
                options: { mod_filter: row.id },
              });
            }}
          >
            Repair…
          </button>`
        }
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

  // The selection's size in words, shared by the batch bar and the live
  // region below.
  const selectionCount = `${selectedRows().length} of ${visible.length} selected`;
  // What the live region says is recomputed only when the SELECTION
  // changes. The count above also moves with `visible`, so a region reading
  // it directly spoke up on every keystroke of a search - "0 of 1 selected"
  // while nothing about the selection had changed. The words are the ones
  // the bar shows at the moment of the change. A hook, so it is called
  // here, above the returns below: Preact matches hook state by call order.
  const selectionAnnouncement = useMemo(
    () => (selected.size > 0 ? selectionCount : "No mods selected"),
    [selected],
  );

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

  // Issue 440: what the profile lists, switched off, and never downloaded.
  const listedOff = html`<${ListedOffList}
    refs=${listedOffRefs(mods)}
    state=${state}
    actions=${actions}
    heading="Listed in this profile, switched off, not downloaded"
  />`;

  if ((mods.mods ?? []).length === 0) {
    return html`
      <section class="library">
        <h2 class="section-header">Library</h2>
        ${listedOff}
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

  // issue 434: what the select-all box is about to do, counted. The
  // accessible name says the NUMBER because that is the surprise the issue
  // is about - "Update 40 mods" should never be the first time you learn
  // there were forty - and it names the direction, because the same control
  // clears the selection once it is full. Both start with the visible word
  // (WCAG 2.5.3, label in name), as the Updates card's box does.
  const selectAllLabel = allTaken
    ? `Select all: all ${countOf(selectable.length, "mod")} in view selected, press to clear`
    : `Select all ${countOf(selectable.length, "mod")} in view`;
  // The one thing the count alone cannot explain: rows that are on screen
  // and deliberately not taken.
  const skipped = visible.length - selectable.length;
  const selectAllTitle =
    skipped > 0
      ? `${countOf(skipped, "row")} managed by Steam ${skipped === 1 ? "is" : "are"} not included — lmm cannot enable or disable ${skipped === 1 ? "it" : "them"}`
      : undefined;

  return html`
    <section class="library">
      ${
        // issue 434: a selection change is announced - pressing "a" changes
        // it with nothing else to say so. The region is ALWAYS rendered,
        // rather than being the batch bar's own count: the bar mounts on the
        // first selection, and a live region inserted together with its text
        // is not reliably announced, which would silence exactly that first
        // press. "No mods selected" is what a clear announces; as the
        // region's first content it is not read out at all.
        ""
      }
      <p class="visually-hidden" role="status" data-testid="selection-status">
        ${selectionAnnouncement}
      </p>
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
          data-action="check-updates"
          disabled=${checking}
          aria-busy=${checking ? "true" : null}
          onClick=${checkForUpdates}
        >
          ${checking ? "Checking…" : "Check for updates"}
        </button>
        <${InlineJob}
          origin=${UPDATE_ALL_ORIGIN}
          state=${state}
          actions=${actions}
        >
          <button
            type="button"
            class="button button--small"
            data-action="update-all"
            disabled=${updatableRows().length === 0}
            title=${
              updatableRows().length === 0
                ? "No mod in this profile has an update lmm can apply"
                : undefined
            }
            onClick=${updateAll}
          >
            ${`Update all (${updatableRows().length})`}
          </button>
        <//>
        <button
          type="button"
          class="button button--small"
          data-action="verify"
          disabled=${verifying}
          aria-busy=${verifying ? "true" : null}
          onClick=${verifyAll}
        >
          ${verifying ? "Verifying…" : "Verify"}
        </button>
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
                    <th class="col--select">
                      ${
                        // issue 434: the select-all, wrapped in the column's
                        // own heading so the word still labels the column
                        // (owner demo 1 asked for a real heading here) and
                        // doubles as the box's click target. aria-label
                        // overrides that word for the accessible name,
                        // because "Select" says nothing about what this one
                        // press is about to take.
                        ""
                      }
                      <label class="library__select-all">
                        <input
                          type="checkbox"
                          data-testid="select-all"
                          checked=${allTaken}
                          indeterminate=${takenCount > 0 && !allTaken}
                          disabled=${selectable.length === 0}
                          aria-label=${selectAllLabel}
                          title=${selectAllTitle}
                          onChange=${toggleSelectAll}
                        />
                        Select
                      </label>
                    </th>
                    <th class="col--enabled">Enabled</th>
                    <th class="col--name">Name</th>
                    <th class="col--version">Version</th>
                    <th class="col--author">Author</th>
                    <th class="col--source">Source</th>
                    <th class="col--badges">Badges</th>
                    <th class="col--order">Load order</th>
                    <th class="col--method">Method</th>
                    <th class="col--installed">Installed</th>
                    <th class="col--menu">
                      <span class="visually-hidden">Actions</span>
                    </th>
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
                    // issue 432: what the user asked this row's toggle for
                    // and has not got yet (toggleack.js). While it is set the
                    // box renders the REQUESTED value rather than the
                    // server's, so the click lands visibly in the frame it
                    // was made in.
                    const requested = requestedFor(row);
                    const togglePending = requested !== undefined;
                    return html`
                      <tr
                        key=${row.key}
                        data-mod=${row.key}
                        class="mod-row ${selected.has(row.key) ? "mod-row--selected" : ""} ${togglePending ? "mod-row--pending" : ""}"
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
                            aria-label=${
                              // issue 379: an external row's box cannot
                              // act, so its accessible name must not offer
                              // an action - it states what IS, and the
                              // title carries the remedy.
                              row.isExternal
                                ? `${row.name} is managed by Steam`
                                : `${row.enabled ? "Disable" : "Enable"} ${row.name}`
                            }
                            checked=${togglePending ? requested.want : row.enabled}
                            disabled=${
                              // issue 379: EXTERNAL rows are gated here too,
                              // matching updatableSelectedRows and the row
                              // menu. Enable/disable is refused by core for
                              // a Steam Workshop item - Steam owns whether
                              // it loads - so a live checkbox on the primary
                              // screen could only ever start a job that
                              // fails, which is the same defect the deploy
                              // dry run had. Disabled-with-a-reason is the
                              // full mod page's rollback pattern.
                              //
                              // issue 432: the box is still frozen while the
                              // request is in flight - a second, contrary
                              // job over the same mod is not a thing to
                              // offer - but it is now frozen HOLDING THE
                              // REQUESTED VALUE, with the row saying what is
                              // happening, instead of frozen on the old one
                              // saying nothing at all.
                              Boolean(mutation) ||
                              togglePending ||
                              row.isExternal
                            }
                            aria-busy=${togglePending ? "true" : null}
                            title=${
                              row.isExternal
                                ? "Steam manages this item — unsubscribe in Steam, or use the game's own mod menu"
                                : undefined
                            }
                            onClick=${(e) => e.stopPropagation()}
                            onChange=${() => toggleEnabled(row)}
                          />
                          ${
                            togglePending &&
                            html`<span
                              class="toggle-pending"
                              aria-hidden="true"
                            ></span>`
                          }
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
                            // The row's own live line: the running job's
                            // words once there IS a job, and - issue 432 -
                            // the requested change's own words for the
                            // window before that, which is exactly the
                            // window the user was clicking into.
                            mutation
                              ? html`<span class="mod-row__live" role="status">
                                  ${[
                                    mutationLabel(mutation.summary.kind),
                                    progressText(mutation.frame),
                                  ]
                                    .filter(Boolean)
                                    .join(" · ")}
                                </span>`
                              : togglePending &&
                                html`<span
                                  class="mod-row__live"
                                  role="status"
                                  data-testid="toggle-pending"
                                  >${pendingToggleLabel(requested.want)}</span
                                >`
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
                            // issue 418: the row says its health state
                            // WHICHEVER state it is in. It used to carry a
                            // ⚠ when something was wrong and nothing at all
                            // otherwise - so "no badge" meant both "checked,
                            // fine" and "never checked", which are opposite
                            // things to tell someone about their install.
                            //
                            // role="img" with the sentence as its name: the
                            // glyph alone means nothing to a screen reader,
                            // and a role-less span exposes no name at all -
                            // `title` stays for the pointer, but it is
                            // neither a keyboard nor a touch affordance.
                            html`<span
                              class="badge ${healthBadgeTone(row)}"
                              data-testid="row-health"
                              data-health=${row.healthState}
                              role="img"
                              aria-label=${healthLabel(row)}
                              title=${healthLabel(row)}
                              >${healthBadge(row)}</span
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
                          ${
                            // issue 417: the per-mod Update, in the open.
                            // Not wrapped in InlineJob like the header's own
                            // "Update all" - this cell is too narrow for a
                            // progress bar, and the row already reports its
                            // own running job on the name column's live line
                            // (the library's established pattern for a
                            // row-level job). It is refused while one is
                            // running so a second cannot be stacked on it.
                            row.hasUpdate &&
                            !row.isExternal &&
                            html`<button
                              type="button"
                              class="button button--small button--primary"
                              data-action="row-update"
                              disabled=${Boolean(mutation)}
                              aria-label=${`Update ${row.name} to ${row.updateTarget}`}
                              onClick=${() => updateRow(row)}
                            >
                              Update
                            </button>`
                          }
                          <button
                            type="button"
                            class="button button--small"
                            data-action="row-menu"
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
      ${listedOff}
      ${
        selected.size > 0 &&
        html`
          <div class="batch-bar">
            <span class="batch-bar__count">${selectionCount}</span>
            <button
              type="button"
              class="button"
              data-action="batch-enable"
              disabled=${togglableSelectedRows().length === 0}
              title=${toggleRefusalTitle("enable")}
              onClick=${() => batchEnable("enable")}
            >
              ${`Enable (${togglableSelectedRows().length})`}
            </button>
            <button
              type="button"
              class="button"
              data-action="batch-disable"
              disabled=${togglableSelectedRows().length === 0}
              title=${toggleRefusalTitle("disable")}
              onClick=${() => batchEnable("disable")}
            >
              ${`Disable (${togglableSelectedRows().length})`}
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
              ${`Update (${updatableSelectedRows().length})`}
            </button>
            <button
              type="button"
              class="button button--danger"
              data-action="batch-uninstall"
              onClick=${batchUninstall}
            >
              ${`Uninstall (${selectedRows().length})`}
            </button>
          </div>
        `
      }
    </section>
  `;
}
