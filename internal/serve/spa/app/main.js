// main.js - the SPA entry point the shell loads as a module.
//
// It wires the router, the store, the theme and the API client into one
// render loop, and mounts the root component over the shell's placeholder.

import { render } from "./render.js";
import { html } from "./render.js";
import { App } from "./components/app.js";
import { createStore } from "./store.js";
import { parseLocation, onRouteChange, navigate } from "./router.js";
import { currentTheme, setTheme } from "./theme.js";
import {
  get,
  scoped,
  plan as planMutation,
  startJob,
  startToggle as startToggleJob,
  setModLock as apiSetModLock,
  clearModLock as apiClearModLock,
  setModUpdatePolicy as apiSetModUpdatePolicy,
  getModDetail,
  getModFiles,
  getModVersions,
  search as apiSearch,
  ApiError,
} from "./api.js";
import { resolveGamePath } from "./navigation.js";
import {
  connectActivity,
  isOriginMounted,
  mountedOriginsSnapshot,
  registerOrigin,
} from "./activity.js";

const store = createStore();
const root = document.getElementById("app");

// The shell's own placeholder (index.html's "Loading…" paragraph) is plain
// static markup Preact does not own. Preact's render() diffs its vnode tree
// against what IT previously rendered into a container - on the very first
// call there is nothing to diff against, so it simply inserts its output
// alongside whatever is already there rather than replacing it. Clearing
// the container once, before that first render, is what makes main.js the
// sole owner of #app from here on; every render after this one is a normal
// Preact-to-Preact diff and needs no further help.
root.replaceChildren();

function draw() {
  render(
    html`<${App}
      state=${store.get()}
      onThemeChange=${() => draw()}
      actions=${actions}
    />`,
    root,
  );
}

/** Unwraps a Promise.allSettled result, or null for a rejected one - a
 * failure in any ONE of Mission Control's four supplementary documents must
 * not blank the three that loaded fine. */
function settled(result) {
  return result.status === "fulfilled" ? result.value : null;
}

/** The message half of a settled() null - undefined for a fulfilled result,
 * so a component can tell "loaded, nothing to report" from "the fetch
 * failed" without inspecting the raw PromiseSettledResult itself. */
function failureMessage(result) {
  if (result.status === "fulfilled") return null;
  const err = result.reason;
  return err instanceof Error ? err.message : String(err);
}

/**
 * Redirects away from the chooser when it has nowhere real to choose from:
 * a single configured game, or one explicitly marked default among several
 * (docs/plans/2026-08-31-serve-spa-design.md §Information architecture: "/
 * -> game chooser (or redirect to the single/default game)"). The target
 * profile is resolved the same way the top bar's game picker resolves one
 * (resolveGamePath); a game with no active profile yet is left for the
 * chooser to render rather than routed into a context that can't resolve.
 */
async function maybeRedirectFromChooser(games) {
  const list = games ?? [];
  const target = list.length === 1 ? list[0] : list.find((g) => g.is_default);
  if (!target) return;

  const path = await resolveGamePath(target.id).catch(() => null);
  if (path) navigate(path, { replace: true });
}

// hydrateSeq is the fence every route-scoped write in this module passes
// before it lands (C-1, epic live review).
//
// hydrate() is not the only thing that starts one: onJobDone re-hydrates on
// every completed job, reading the route at THAT instant, while the profile
// picker's own navigate is deferred to a microtask - so a confirmed switch
// leaves two hydrations racing, one under the profile being left and one
// under the profile moved to, both writing the same four store slots. The
// store belonged to whichever settled last, which meant Mission Control
// could render the previous profile's mods, updates, health and conflicts
// under the new profile's URL: the wrong-context bug class path-based
// routing exists to prevent, re-entering through the store instead of the
// URL.
//
// A monotonic counter rather than a route-key comparison, for the case a
// route key cannot see: TWO hydrations of the SAME route (a job completing
// while a slice retry is in flight) are equally capable of landing out of
// order, and only the newest one's answer is current. Claiming a number
// invalidates every claim before it, whatever route it was made for.
let hydrateSeq = 0;

/** beginHydration claims the next fence number, invalidating every write
 * still in flight behind an older one. */
function beginHydration() {
  hydrateSeq += 1;
  return hydrateSeq;
}

/** isCurrentHydration reports whether seq is still the newest claim - the
 * check a slice reload (which claims no number of its own) makes so a route
 * change during its fetch drops its answer rather than writing another
 * context's document into the store. */
function isCurrentHydration(seq) {
  return seq === hydrateSeq;
}

/** commitHydration writes patch into the store only while seq is still the
 * newest claim, and reports whether it did - so a caller with more work to
 * do after the write can stop instead of continuing to fetch for a route
 * nobody is looking at. */
function commitHydration(seq, patch) {
  if (!isCurrentHydration(seq)) return false;
  store.set(patch);
  return true;
}

/**
 * Loads the documents the current route needs.
 *
 * A failed load is put in the store rather than thrown: the shell is
 * already on screen, and an unreachable endpoint should say so in place,
 * not blank the page. The chooser and home routes both need the full,
 * UNSCOPED game list (the chooser to render its cards, home for the top
 * bar's game picker) alongside whatever status the route itself is scoped
 * to - two cheap reads rather than one endpoint trying to answer both
 * questions.
 */
async function hydrate(route) {
  const seq = beginHydration();

  if (route.view === "chooser") {
    try {
      const status = await get("/api/v1/status");
      if (!commitHydration(seq, { status, games: status.games, error: null }))
        return;
    } catch (err) {
      const message = err instanceof ApiError ? err.message : String(err);
      commitHydration(seq, { status: null, games: null, error: message });
      return;
    }
    await maybeRedirectFromChooser(store.get().games);
    return;
  }

  const context = { game: route.game, profile: route.profile };
  try {
    const [status, allStatus] = await Promise.all([
      get(scoped("/api/v1/status", context)),
      get("/api/v1/status"),
    ]);
    const committed = commitHydration(seq, {
      status,
      games: allStatus.games,
      error: null,
      fetchErrors: { ...store.get().fetchErrors, status: null },
    });
    if (!committed) return;
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    // A RE-hydrate (main.js's onJobDone runs this on every job completion,
    // not just a route change) that fails must not blank a page that
    // already has a status - that would take the inline outcome and the
    // tray down with it over a fetch that has nothing to do with either.
    // The fatal `error` slice is reserved for the FIRST load, where there
    // is nothing on screen yet to protect (the I3 rule, applied here).
    if (store.get().status) {
      commitHydration(seq, {
        fetchErrors: { ...store.get().fetchErrors, status: message },
      });
      return;
    }
    commitHydration(seq, { status: null, games: null, error: message });
    return;
  }

  if (route.view === "mod") {
    await hydrateModPage(route, context, seq);
    return;
  }
  if (route.view === "search") {
    await runSearchPage({ query: route.q ?? "", page: 0 });
    return;
  }
  if (route.view !== "home") return;

  // The jobs index is NOT fetched here: GET /api/v1/events opens with a
  // snapshot of every retained job and maintains it from there
  // (activity.js), so a per-route poll could only ever disagree with the
  // live stream about what the machine is doing.
  const [mods, updates, health, conflicts] = await Promise.allSettled([
    get(scoped("/api/v1/mods", context)),
    get(scoped("/api/v1/updates", context)),
    get(scoped("/api/v1/health", context)),
    get(scoped("/api/v1/conflicts", context)),
  ]);
  commitHydration(seq, {
    mods: settled(mods),
    updates: settled(updates),
    health: settled(health),
    conflicts: settled(conflicts),
    fetchErrors: {
      mods: failureMessage(mods),
      updates: failureMessage(updates),
      health: failureMessage(health),
      conflicts: failureMessage(conflicts),
    },
  });
}

/**
 * Loads the full mod page's documents.
 *
 * core.ModFilesReport is the PRIMARY, FATAL-on-failure read - not
 * ModDetail: ModFilesReport.Mod is the INSTALLED record (persisted at
 * install time), so identity/files render for a mod whose SOURCE has since
 * gone away (unregistered, offline, a local/adopted mod with nothing live
 * to ask), the same mod a still-installed row in the library keeps
 * showing. ModDetail is a LIVE source read - the only source of the full
 * description, the changelog, and (because ModDetail composes lock/policy
 * alongside its live GetMod call - internal/core/moddetail.go) this page's
 * own lock-aware version-table gating - so it is fetched alongside the
 * versions document, both I3-style: a source that cannot answer degrades
 * this page's EXTRAS, never blanks the identity/files a real install
 * record already answered for.
 *
 * modPage.key ("source/id") fences a stale write the same way modalSeq
 * does: a slow fetch for a mod the user has already arrowed away from must
 * not land on the page after the faster one for the mod now on screen.
 *
 * A RE-hydrate (onJobDone runs this on every job completion, not just a
 * route change) for the mod ALREADY on screen must not blank it - the same
 * rule hydrate()'s own status guard applies to Mission Control (I1). Only a
 * genuine navigation to a DIFFERENT mod resets to the loading state; the
 * existing filesReport otherwise survives until the new fetch resolves one
 * way or the other, which is what keeps this page's own InlineJob outcome
 * on screen through the re-hydrate a job's own completion triggers.
 */
async function hydrateModPage(route, context, seq = hydrateSeq) {
  const key = `${route.sourceID}/${route.modID}`;
  const reHydrating = store.get().modPage?.key === key;
  if (!reHydrating) {
    store.set({ modPage: { key, filesReport: null, error: null } });
  }

  let filesReport;
  try {
    filesReport = await getModFiles(route.sourceID, route.modID, context);
  } catch (err) {
    if (store.get().modPage?.key !== key) return;
    if (reHydrating) return;
    const message = err instanceof ApiError ? err.message : String(err);
    store.set({ modPage: { key, filesReport: null, error: message } });
    return;
  }
  if (store.get().modPage?.key !== key) return;
  store.set({
    modPage: {
      key,
      filesReport,
      error: null,
      detail: null,
      detailError: null,
      versions: null,
      versionsError: null,
      updates: null,
    },
  });

  // updates joins the versions table against the ONE version
  // CheckGameUpdates would actually land this mod on (C1) - fetched
  // alongside detail/versions, both I3-style: a source that cannot answer
  // degrades this page's EXTRAS, never blanks the identity/files a real
  // install record already answered for.
  //
  // mods/health/conflicts join them since I-5 (epic live review): the full
  // mod page now carries this mod's lock and update policy, its Uninstall,
  // its findings and its conflicts, and all four are answered by those
  // three PROFILE-scoped documents rather than by anything mod-specific on
  // the wire. They land in the TOP-LEVEL store slots, not under modPage,
  // because they are the same documents Mission Control renders for the
  // same context - a second copy under another key is how two surfaces
  // come to disagree about one profile. That also means arriving here from
  // a cold deep link warms them for the "Back to library" that follows.
  const [detail, versions, updates, mods, health, conflicts] =
    await Promise.allSettled([
      getModDetail(route.sourceID, route.modID, context),
      getModVersions(route.sourceID, route.modID, context),
      get(scoped("/api/v1/updates", context)),
      get(scoped("/api/v1/mods", context)),
      get(scoped("/api/v1/health", context)),
      get(scoped("/api/v1/conflicts", context)),
    ]);
  if (store.get().modPage?.key !== key) return;
  if (!isCurrentHydration(seq)) return;
  store.set({
    modPage: {
      ...store.get().modPage,
      detail: settled(detail),
      detailError: failureMessage(detail),
      versions: settled(versions),
      versionsError: failureMessage(versions),
      updates: settled(updates),
    },
    mods: settled(mods) ?? store.get().mods,
    health: settled(health) ?? store.get().health,
    conflicts: settled(conflicts) ?? store.get().conflicts,
    fetchErrors: {
      ...store.get().fetchErrors,
      mods: failureMessage(mods),
      health: failureMessage(health),
      conflicts: failureMessage(conflicts),
    },
  });
}

/**
 * Re-fetches one of Mission Control's four supplementary documents in
 * isolation - the retry affordance a failed card/library offers, and (for
 * health) the spec's "re-run" control on a card that loaded fine. Scoped to
 * the CURRENT route at call time, not the route hydrate() was originally
 * called for, so a retry clicked after a game/profile switch can't write a
 * stale document into the new context.
 */
async function reload(key, path) {
  // Reads the CURRENT fence number without claiming one (C-1): a slice
  // retry is not a hydration and must not invalidate one, but its own
  // answer is just as capable of landing after a route change as
  // hydrate()'s was - scoping the REQUEST to the route at call time only
  // decides which document is fetched, never which route is on screen when
  // it comes back.
  const seq = hydrateSeq;
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  try {
    const value = await get(scoped(path, context));
    commitHydration(seq, {
      [key]: value,
      fetchErrors: { ...store.get().fetchErrors, [key]: null },
    });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    commitHydration(seq, {
      fetchErrors: { ...store.get().fetchErrors, [key]: message },
    });
  }
}

/**
 * reloadStatus re-fetches the scoped and unscoped core.StatusReport
 * documents (issue 332) - the same pair hydrate() fetches on a route change,
 * exposed as its own action for the profiles modal: TopBar's game/profile
 * pickers read status.profiles/games (main.js#GamePicker, ProfilePicker),
 * a document the profiles modal never itself fetches, so a create/rename/
 * delete/set-default there would otherwise leave the top bar stale until
 * the next unrelated re-hydrate. Always treated as a RE-load (fetchErrors,
 * never the fatal `error` slice) - this action only ever runs from a modal
 * that could not be open unless Mission Control had already loaded once.
 */
async function reloadStatus() {
  const seq = hydrateSeq;
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  try {
    const [status, allStatus] = await Promise.all([
      get(scoped("/api/v1/status", context)),
      get("/api/v1/status"),
    ]);
    commitHydration(seq, {
      status,
      games: allStatus.games,
      fetchErrors: { ...store.get().fetchErrors, status: null },
    });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    commitHydration(seq, {
      fetchErrors: { ...store.get().fetchErrors, status: message },
    });
  }
}

// SEARCH_PAGE_SIZE is the dedicated search page's per-page request size -
// the issue 331 pagination the omnibar's own live filter never needs.
const SEARCH_PAGE_SIZE = 20;

// OMNIBAR_FANOUT_LIMIT caps how many rows the omnibar's inline fan-out ever
// appends (M8, unit 5 fix wave): the omnibar sets no page/pageSize at all
// (a live filter has no "next page"), so with no limit either, a real
// multi-source catalog (NexusMods' default page alone can run past a
// hundred hits) could append an unbounded list below the library. The
// dedicated search page is the escape hatch for "more than this" - it
// pages, this never does.
const OMNIBAR_FANOUT_LIMIT = 20;

// omnibarSeq fences the omnibar's fan-out the same way modalSeq fences a
// plan: a slow search whose query the user has since typed past (or
// re-searched) must not land after a newer one.
let omnibarSeq = 0;

/**
 * searchSources fans the omnibar's current text out to the game's sources
 * (design doc §Search: "Enter fans out ... appends 'From sources (n)' rows
 * in place"). A blank query clears whatever fan-out is showing rather than
 * searching for nothing.
 */
async function searchSources(query) {
  const q = (query ?? "").trim();
  omnibarSeq += 1;
  const seq = omnibarSeq;
  if (!q) {
    store.set({ omnibarSearch: null });
    return;
  }
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  store.set({
    omnibarSearch: { status: "loading", query: q, report: null, error: null },
  });
  try {
    const report = await apiSearch(q, { limit: OMNIBAR_FANOUT_LIMIT }, context);
    if (omnibarSeq !== seq) return;
    store.set({
      omnibarSearch: { status: "ready", query: q, report, error: null },
    });
  } catch (err) {
    if (omnibarSeq !== seq) return;
    store.set({
      omnibarSearch: {
        status: "error",
        query: q,
        report: null,
        error: err instanceof ApiError ? err.message : String(err),
      },
    });
  }
}

// searchPageSeq fences the dedicated search page's own fetches the same way
// omnibarSeq fences the inline fan-out - a slow page 1 must not land after a
// faster page 2 (or after the user has navigated to a different query).
let searchPageSeq = 0;

/** emptyFacets is searchPage.facets' shape before anything has loaded - the
 * category/source SELECTs render no options until a real report answers. */
const emptyFacets = { categories: [], sourceIDs: [] };

/** facetsFromReport derives the category/source filter SELECTs' own option
 * lists from a report's hits. Called only for an UNFILTERED report (no
 * category/source applied) - see runSearchPage's own comment on why. */
function facetsFromReport(report) {
  const hits = report.mods ?? [];
  return {
    categories: [
      ...new Set(hits.map((h) => h.category).filter(Boolean)),
    ].sort(),
    sourceIDs: [...new Set(hits.map((h) => h.source_id))].sort(),
  };
}

/**
 * runSearchPage loads one page of the dedicated /search route (design doc
 * §Search: "a dedicated search page ... pagination"). Called by hydrate()
 * on route entry/deep link and by the page's own Next/Prev/category/source
 * controls - none touch the URL's ?q=, which stays the query alone
 * (pagination and filtering are client-driven state, not part of the
 * route).
 *
 * category/source are Important 1b's own fix (unit 5 fix wave): moved
 * SERVER-SIDE from a client-side slice of one page's own hits, so the count
 * this renders answers the CATALOG for that filter, not one page of it. A
 * filter change always re-queries PAGE 0 (searchPageSetCategory/
 * searchPageSetSource below) - a filtered page 3 carried over from an
 * unfiltered browse would be a page number with nothing behind it.
 *
 * facets (the category/source SELECTs' own option lists) are derived ONLY
 * from an UNFILTERED report (no category/source of its own) and then
 * RETAINED across a filter change for the same query, rather than
 * recomputed from whatever the filtered report happens to hold: a report
 * already narrowed to "Armor" carries no "Weapons" hits at all, so
 * recomputing from it would make every OTHER category vanish from its own
 * picker the moment one was chosen. Reset only on a genuinely new query.
 */
async function runSearchPage({ query, page, category = "", source = "" }) {
  const q = (query ?? "").trim();
  searchPageSeq += 1;
  const seq = searchPageSeq;
  if (!q) {
    store.set({ searchPage: null });
    return;
  }
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  const previous = store.get().searchPage;
  const facets =
    previous?.query === q && previous.status !== "error"
      ? previous.facets
      : emptyFacets;

  store.set({
    searchPage: {
      status: "loading",
      query: q,
      page,
      pageSize: SEARCH_PAGE_SIZE,
      category,
      source,
      report: null,
      error: null,
      facets,
    },
  });
  try {
    const report = await apiSearch(
      q,
      { page, pageSize: SEARCH_PAGE_SIZE, category, source },
      context,
    );
    if (searchPageSeq !== seq) return;
    const nextFacets = !category && !source ? facetsFromReport(report) : facets;
    store.set({
      searchPage: {
        status: "ready",
        query: q,
        page,
        pageSize: SEARCH_PAGE_SIZE,
        category,
        source,
        report,
        error: null,
        facets: nextFacets,
      },
    });
  } catch (err) {
    if (searchPageSeq !== seq) return;
    store.set({
      searchPage: {
        status: "error",
        query: q,
        page,
        pageSize: SEARCH_PAGE_SIZE,
        category,
        source,
        report: null,
        error: err instanceof ApiError ? err.message : String(err),
        facets,
      },
    });
  }
}

/** searchPageGoTo re-runs the search page at a different page, keeping the
 * current query/category/source - the Next/Prev controls' own action. */
function searchPageGoTo(page) {
  const current = store.get().searchPage;
  if (!current) return;
  runSearchPage({
    query: current.query,
    page,
    category: current.category,
    source: current.source,
  });
}

/** searchPageSetCategory/searchPageSetSource apply a new filter at page 0 -
 * the category/source SELECTs' own onChange (Important 1b). */
function searchPageSetCategory(category) {
  const current = store.get().searchPage;
  if (!current) return;
  runSearchPage({
    query: current.query,
    page: 0,
    category,
    source: current.source,
  });
}

function searchPageSetSource(source) {
  const current = store.get().searchPage;
  if (!current) return;
  runSearchPage({
    query: current.query,
    page: 0,
    category: current.category,
    source,
  });
}

// modalSeq fences a slow plan against a modal that is no longer open. Each
// openPlan takes the next number; the response only writes itself into the
// store if that number is still current, so a plan that arrives after the
// user pressed Cancel (or opened a different one) is dropped rather than
// re-opening a modal nobody asked for.
let modalSeq = 0;

/**
 * Opens the confirm-plan modal for one mutation: computes the plan
 * (POST /api/v1/plans/{kind}) and puts the document on screen.
 *
 * Nothing has mutated when this resolves - that is the whole point of the
 * Plan/Apply split (docs/plans/2026-08-30-serve-design.md). origin is the
 * key the initiating control morphs on once the job starts; title and
 * confirmLabel are that control's own words for what it is about to do.
 */
async function openPlan({
  kind,
  origin,
  title,
  confirmLabel,
  options,
  applyOptions,
  onConfirmed,
  openerSelector,
}) {
  modalSeq += 1;
  const seq = modalSeq;
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  // options (the PLAN-time request) is retained on the modal, not just sent:
  // install's conflict round trip (retryInstallOverwrite) needs it to
  // re-plan the identical mutation once a job it started fails with
  // *core.ConflictError, and nothing else remembers what was asked for
  // after planMutation's request has already gone out.
  // type: "plan" marks this modal's shape in the shared modal slot
  // (store.js's own doc comment): confirmplan.js, reordermodal.js and
  // profilesmodal.js each self-guard on it, so app.js can mount all three
  // as siblings without a switch statement of its own.
  //
  // onConfirmed (m4/I2, unit 6 fix wave) is an optional callback run once
  // this plan's job has actually STARTED (confirmPlan, below) - the
  // library batch bar's own way of clearing its multi-select only once the
  // mutation is truly underway, never at open time, which used to tear the
  // very control the user just clicked out of the DOM before the modal
  // even finished mounting (the same removal that broke focus-return, I2).
  //
  // openerSelector (issue 334) is modal.js's own focus-return escape hatch,
  // carried on the modal rather than passed at the Modal call site: a plan
  // opened from a DROPDOWN (the profile picker's "Switch and deploy…") has
  // no opener left to focus by the time the modal unmounts, because the
  // menu closed to let the modal open. Every other caller omits it and gets
  // the captured activeElement, which is still on screen for them.
  const base = {
    type: "plan",
    kind,
    origin,
    title,
    confirmLabel,
    seq,
    options,
    // applyOptions is normally undefined at open time and filled in by the
    // renderer's own controls (setPlanOptions). It is carried HERE for
    // replanWith (below), which re-opens the same plan with a changed
    // plan-time option and must not throw away an apply-time choice the
    // user already made in the same modal.
    applyOptions,
    onConfirmed,
    openerSelector,
  };

  store.set({ modal: { ...base, status: "planning" } });
  try {
    const response = await planMutation(kind, options, context);
    if (modalSeq !== seq) return;
    store.set({
      modal: {
        ...base,
        status: "ready",
        planID: response.plan_id,
        plan: response.plan,
      },
    });
  } catch (err) {
    if (modalSeq !== seq) return;
    store.set({ modal: { ...base, status: "error", ...describe(err) } });
  }
}

/**
 * replanWith re-computes the OPEN plan with one plan-time option changed
 * (C-3, epic live review).
 *
 * A plan-time option is one that changes what the plan SAYS - install's
 * show_archived and no_deps, uninstall's keep_cache, deploy's link method.
 * Patching it locally would leave the preview on screen describing a
 * mutation other than the one Confirm submits, which is the exact dishonesty
 * the Plan/Apply split exists to prevent. So the modal re-plans: same kind,
 * same origin, same words, a new plan.
 *
 * applyPatch moves an apply-time twin along with it, for the fields that
 * live in both halves of a kind's request (kind_uninstall.go/kind_purge.go
 * each take keep_cache/uninstall/skip_hooks twice - plan-time so the preview
 * is honest, apply-time because the flow reads its own options).
 */
async function replanWith(planPatch, applyPatch) {
  const modal = store.get().modal;
  if (!modal || modal.type !== "plan") return;
  await openPlan({
    kind: modal.kind,
    origin: modal.origin,
    title: modal.title,
    confirmLabel: modal.confirmLabel,
    options: { ...(modal.options ?? {}), ...planPatch },
    applyOptions: { ...(modal.applyOptions ?? {}), ...(applyPatch ?? {}) },
    onConfirmed: modal.onConfirmed,
    openerSelector: modal.openerSelector,
  });
}

/** closeModal closes whatever the shared modal slot currently holds -
 * generic over every shape it can carry (store.js's own doc comment), not
 * just the confirm-plan kind openPlan/confirmPlan manage. modalSeq still
 * advances even for a non-plan modal: it costs nothing, and it means a
 * plan that happened to be mid-flight when a DIFFERENT modal type closed
 * (there is at most one open at a time, so this can only be this same
 * modal) is fenced the same way Cancel has always fenced it. */
function closeModal() {
  modalSeq += 1;
  store.set({ modal: null });
}

/** openReorderModal opens the reorder modal over profileName - reachable
 * from the library's own "Reorder…" control and every Conflicts-card row's
 * "Resolve…" (design doc §Modals, issue 332). Not a Plan/Apply mutation
 * (reordermodal.js's own header comment), so there is no plan to compute
 * here - the modal fetches its own live preview once mounted. */
function openReorderModal({ profileName, focusKey }) {
  store.set({ modal: { type: "reorder", profileName, focusKey } });
}

/** openProfilesModal opens the profiles modal - the top bar's own "Manage
 * profiles…" (design doc §Modals, issue 332). profilesmodal.js reloads its own
 * listing (actions.reloadProfiles) once mounted; nothing to precompute
 * here either. */
function openProfilesModal() {
  store.set({ modal: { type: "profiles" } });
}

/** openShortcutsModal opens the keyboard-shortcuts help (design doc §Modals,
 * issue 334's gate review Important 3) - reachable from the top bar's own
 * "?" control and from the `?` key on any screen. Read-only: it renders
 * shortcuts.js's rows and has nothing to fetch or precompute.
 *
 * openerSelector names the top-bar control rather than trusting the captured
 * activeElement, because the `?` key can open this from anywhere - including
 * with focus on an element the modal's own confirm may have removed. */
function openShortcutsModal() {
  store.set({
    modal: { type: "shortcuts", openerSelector: '[data-action="shortcuts"]' },
  });
}

/** openUninstallBatchModal opens the library batch bar's "Uninstall
 * selected" (issue 332): ONE confirm modal over N selected mods' own uninstall
 * plans, never a second modal per mod ("modals stack at most one deep",
 * design doc §Modals). uninstallbatchmodal.js plans every mod itself once
 * mounted; this only records WHICH mods. onConfirmed mirrors openPlan's own
 * (m4/I2, unit 6 fix wave) - run once the batch has actually started, not
 * at open time, so the caller's own selection/opener survives a Cancel. */
function openUninstallBatchModal(mods, onConfirmed) {
  store.set({ modal: { type: "uninstall-batch", mods, onConfirmed } });
}

// bindingJobs is every in-flight POST /api/v1/jobs (or plan-free toggle
// start), keyed by the origin that started it. Each entry resolves only
// AFTER that origin's binding has been written to the store, which is what
// onJobDone waits on: a job can finish before the response that names it
// has even been read (a deploy whose before_all hook exits immediately does
// exactly that), and a completion looked up before its binding lands finds
// no origin and toasts a control that is right there on screen. Observed,
// not hypothetical - it is what the failing-deploy scenario asserts against.
//
// A Map keyed by origin rather than one shared slot (issue 331 carry-in, Unit 3
// review M6): Unit 3 could only ever have one control confirming at a time
// (one modal open), but Unit 5's inline per-row install means two DIFFERENT
// origins ("install:fake/1", "install:fake/2") can each start a job while
// the other's own start is still in flight - a single module-level slot
// would let the second start silently clobber the first's entry, so
// onJobDone's wait for THAT job's own binding would resolve immediately
// instead of actually waiting, exactly the race the single slot existed to
// close. Keying by origin also means two starts from the SAME origin can
// never overlap in the first place - InlineJob unmounts the button the
// instant state.origins[origin] is set, so there is nothing left on screen
// to click a second time.
const bindingJobs = new Map();

// Exposed for the E2E overlap test's own introspection only (Important 2,
// unit 5 fix wave review): TestE2E_OverlappingInstallAndToggleBothTrack
// Correctly's END-STATE assertions (which mod ends up enabled, which job's
// failure lands on which row) pass identically whether bindingJobs is this
// Map or Unit 3's single module-level slot - a scenario timed so the
// install's start is slow and the toggle's is fast never puts a check in
// the window where the two implementations actually disagree (the review's
// own finding, reverting this Map and re-running got 6/6 green). The one
// thing that DOES discriminate them is how many starts are tracked at once
// while both are genuinely still in flight, which no rendered DOM state
// exposes on its own.
if (typeof window !== "undefined") {
  window.__lmmBindingJobsSize = () => bindingJobs.size;

  // Exposed for the GenericPlanView scenario only (issue 334, the m8
  // carry): the fallback renderer's whole contract is about a kind wired
  // BEFORE its renderer exists, so by construction no control in this
  // application ever opens one - every registered kind has a renderer
  // (planrenderers.js). Handing a test the same entry point every control
  // uses is the only way to drive the fallback through the real modal, the
  // real Confirm and a real job rather than asserting about a component in
  // isolation. It adds no capability: openPlan only computes a plan, which
  // is exactly what a POST from the console could already do.
  window.__lmmOpenPlan = (spec) => actions.openPlan(spec);
}

/** startBinding runs work (an async fn returning nothing) as origin's
 * binding: recorded in bindingJobs until it settles, keyed so a concurrent
 * binding for a DIFFERENT origin is never disturbed. Shared by confirmPlan
 * and startToggle - the two entry points that write into state.origins. */
async function startBinding(origin, work) {
  const promise = work();
  bindingJobs.set(origin, promise);
  try {
    await promise;
  } finally {
    if (bindingJobs.get(origin) === promise) bindingJobs.delete(origin);
  }
}

/** awaitBindings snapshots every currently in-flight binding and waits for
 * all of them - onJobDone's own use, since a job that just finished could be
 * the one behind ANY of them, not necessarily the most recent. Snapshotting
 * before awaiting (rather than awaiting bindingJobs.values() live) matches
 * the original single-bindingJob fencing: a binding that STARTS after this
 * call began is a different job's concern, not this completion's. */
function awaitBindings() {
  return Promise.allSettled([...bindingJobs.values()]);
}

// jobDoneWaiters lets one caller await a SPECIFIC job id's terminal summary
// (issue 332's sequenced batches - the library batch bar's enable/disable/
// uninstall, and every per-mod job kind_toggle.go and kind_uninstall.go's
// docs name: "sequence per-mod batch jobs... start the next only after the
// previous job's job_done frame", the wire's own caveat against the 8-job
// running cap AND core's own beginOp serialisation). bindingJobs above
// answers a different question ("is ANY start still in flight") for the
// single global onJobDone toast decision; this answers "has THIS ONE job
// finished" for code that needs to run its own next step only after it has.
const jobDoneWaiters = new Map();

/** waitForJobDone resolves with jobID's own terminal summary. Checks
 * jobsIndex FIRST, synchronously, before ever registering a waiter: the
 * activity stream can deliver a job's job_started AND job_done frames
 * before the POST /api/v1/jobs response that names it has even been read
 * (bindingJobs' own doc comment - "observed, not hypothetical"), so a naive
 * "subscribe, then wait" would miss a job that was already finished by the
 * time this is called. */
function waitForJobDone(jobID) {
  const existing = (store.get().jobsIndex ?? []).find((j) => j.id === jobID);
  if (existing && existing.state !== "running")
    return Promise.resolve(existing);
  return new Promise((resolve) => jobDoneWaiters.set(jobID, resolve));
}

/**
 * startSequencedBatch runs one job per item in `items`, strictly one at a
 * time - the wire's own sequencing caveat - registering every item's origin
 * as "on screen" for the WHOLE batch (activity.js#registerOrigin) so no
 * individual job's completion produces its own toast; a single END-OF-BATCH
 * toast is pushed once every item has settled (design doc's "a single
 * end-of-batch toast" applied to this unit's batch bar). Each item's own
 * origin is left in state.origins afterwards (not cleared), so a row still
 * on screen keeps showing that job's own inline Done/Failed outcome - the
 * "per-row inline progress" half of the same sentence - until the row's own
 * InlineJob is dismissed.
 *
 * `run(item)` starts one item's job and returns its job id; `labelOf(item)`
 * names it for the failure list; `verb` is the toast's own past-tense word
 * ("Enabled", "Disabled", "Uninstalled").
 */
async function startSequencedBatch(
  items,
  { run, originOf: itemOrigin, labelOf, verb },
) {
  const unregisters = items.map((item) => registerOrigin(itemOrigin(item)));
  const failed = [];
  try {
    for (const item of items) {
      const origin = itemOrigin(item);
      try {
        const jobID = await run(item);
        store.set({ origins: { ...store.get().origins, [origin]: jobID } });
        const summary = await waitForJobDone(jobID);
        if (summary.state === "failed") failed.push(labelOf(item));
      } catch (err) {
        failed.push(labelOf(item));
      }
    }
  } finally {
    unregisters.forEach((unregister) => unregister());
  }
  const ok = items.length - failed.length;
  pushToast({
    tone: failed.length > 0 ? "failure" : "success",
    title: `${verb} ${ok}/${items.length} mod${items.length === 1 ? "" : "s"}`,
    detail: failed.length > 0 ? `Failed: ${failed.join(", ")}` : "",
  });
}

/** startBatchToggle sequences an enable/disable job per mod (the library
 * batch bar's Enable/Disable, issue 332) - the same per-mod origin
 * ("mod:{source}/{id}:toggle") the row's own toggle and the slide-over's
 * Enable/Disable button already use (modrows.js#modOriginPattern), so a
 * visible row shows the SAME inline progress whichever control started it. */
async function startBatchToggle(action, mods) {
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await startSequencedBatch(mods, {
    run: (mod) =>
      startToggleJob(action, mod.source_id, mod.id, context).then(
        (r) => r.job_id,
      ),
    originOf: (mod) => `mod:${mod.source_id}/${mod.id}:toggle`,
    labelOf: (mod) => mod.name ?? `${mod.source_id}:${mod.id}`,
    verb: action === "enable" ? "Enabled" : "Disabled",
  });
}

/**
 * startUninstallBatch sequences an uninstall job per mod - never a
 * server-side loop over them (the wire's own "deliberately NOT a new batch
 * endpoint" ruling, task-A report §8).
 *
 * Each mod is RE-PLANNED immediately before its own apply, exactly the same
 * rule kind_updates.go's own Apply loop follows (Ruling 5: a plan is a
 * contract about a world that has not moved). uninstallbatchmodal.js's own
 * preview plans every selected mod UP FRONT so the confirm screen has
 * something to show - but a plan computed before the batch even started is
 * STALE by the time the SECOND mod's turn comes, because the FIRST mod's
 * own uninstall already changed "installed mods" (observed: core's
 * installedSnapshot check refused exactly this with "plan is stale" the
 * first time this was wired straight to the preview's own plan ids).
 * Redeeming the preview's plan_id would only ever work for the batch's
 * first item; re-planning here is what makes every item after it safe.
 */
async function startUninstallBatch(mods) {
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await startSequencedBatch(mods, {
    run: async (mod) => {
      const response = await planMutation(
        "uninstall",
        { mod_id: mod.id, source_id: mod.source_id },
        context,
      );
      const { job_id: jobID } = await startJob(response.plan_id, {});
      return jobID;
    },
    originOf: (mod) => `mod:${mod.source_id}/${mod.id}:uninstall`,
    labelOf: (mod) => mod.name ?? `${mod.source_id}:${mod.id}`,
    verb: "Uninstalled",
  });
}

/** Redeems the open modal's plan handle, starting its Apply as a job
 * (POST /api/v1/jobs) and binding it to the control that opened the modal.
 *
 * The store learns about the job from TWO directions: the job id lands here
 * immediately, so the control can morph on the very next render, while the
 * job's own summary and progress arrive on the activity stream. That is why
 * origins is written here and jobsIndex is not - one writer each. */
async function confirmPlan() {
  const modal = store.get().modal;
  if (!modal || modal.status !== "ready") return;

  store.set({ modal: { ...modal, status: "starting" } });
  await startBinding(modal.origin, async () => {
    try {
      const { job_id: jobID } = await startJob(
        modal.planID,
        modal.applyOptions,
      );
      if (store.get().modal?.seq !== modal.seq) return;
      if (overwriteRetryKinds.has(modal.kind)) {
        rememberInstallRequest(
          modal.origin,
          modal.kind,
          modal.options,
          modal.applyOptions,
        );
      }
      store.set({
        modal: null,
        origins: { ...store.get().origins, [modal.origin]: jobID },
      });
      modal.onConfirmed?.();
    } catch (err) {
      if (store.get().modal?.seq !== modal.seq) return;
      store.set({ modal: { ...modal, status: "error", ...describe(err) } });
    }
  });
}

// overwriteRetryKinds is every plan kind whose *core.ConflictError
// failures.js's nextStepFor implies an Overwrite affordance for - see that
// file's own conflictKinds. install and import_archive (issue 333) both
// take the identical "accept_conflicts" apply-time field, so one retry path
// serves both.
const overwriteRetryKinds = new Set(["install", "import_archive"]);

// installRequests remembers the exact (kind, plan-time, apply-time) request
// behind each ORIGIN that has successfully STARTED a job whose kind is in
// overwriteRetryKinds - the conflict round trip's (failures.js/tray.js) only
// source for what to re-plan once that job fails with *core.ConflictError:
// the failed job's own summary carries the typed envelope, never the
// request that produced it (activity.go's jobSummary has no such field - it
// is the tray's job to offer the next step, not the registry's to remember
// why). Overwritten on every (re-)start of the same origin, so a
// retry-after-retry always answers from the MOST RECENT attempt.
const installRequests = new Map();

function rememberInstallRequest(origin, kind, planOptions, applyOptions) {
  installRequests.set(origin, { kind, planOptions, applyOptions });
}

/**
 * canRetryInstallOverwrite reports whether retryInstallOverwrite has
 * anything to re-plan for jobID (I3, unit 5 fix wave): installRequests is a
 * page-lifetime Map, so a failed install's tray entry - itself server-side
 * and reload-durable via GET /api/v1/jobs - can outlive the ONLY record of
 * what to re-plan. The failed job's own summary carries no source_id/mod_id/
 * options of its own to rebuild it from (activity.go's jobSummary; a failed
 * job's Result is never populated either) - only its typed conflict details,
 * which name the file's CURRENT owner, not the mod that failed to install -
 * so after a reload this is honestly false rather than a guess. The tray
 * button reads this to disable itself instead of silently doing nothing.
 */
function canRetryInstallOverwrite(jobID) {
  const origin = originOf(jobID);
  return Boolean(origin && installRequests.get(origin));
}

/**
 * retryInstallOverwrite answers a failed install's conflict the way v2 Phase
 * 3 Ruling 1 answers every mid-flight decision: not a callback into Apply,
 * but a fresh Plan/Apply re-run with the matching option set - here,
 * accept_conflicts. The re-plan's own Apply finds the cache the refused
 * attempt already warmed (kind_install.go's own doc comment), so it
 * downloads nothing the second time.
 *
 * Routed through startBinding under the SAME origin the original install
 * used: the map-keyed bindingJobs fix (issue 331 carry-in) is what makes this
 * safe to fire while a DIFFERENT row's install is independently in flight -
 * two origins' bindings never see each other.
 */
async function retryInstallOverwrite(jobID) {
  const origin = originOf(jobID);
  const req = origin && installRequests.get(origin);
  if (!req) {
    // Reachable only if something fires this despite canRetryInstallOverwrite
    // saying no (the tray button is disabled in that case) - a toast rather
    // than the silent no-op this used to be (I3): a failure whose next step
    // does nothing at all is worse than one with no next step offered.
    pushToast({
      tone: "failure",
      title: "Can't retry",
      detail:
        "This install must be retried from a fresh attempt - the page was reloaded since it started.",
    });
    return;
  }

  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await startBinding(origin, async () => {
    try {
      const response = await planMutation(req.kind, req.planOptions, context);
      const { job_id: newJobID } = await startJob(response.plan_id, {
        ...req.applyOptions,
        accept_conflicts: true,
      });
      rememberInstallRequest(
        origin,
        req.kind,
        req.planOptions,
        req.applyOptions,
      );
      store.set({ origins: { ...store.get().origins, [origin]: newJobID } });
    } catch (err) {
      pushToast({
        tone: "failure",
        title: "Overwrite failed",
        detail: err instanceof ApiError ? err.message : String(err),
      });
    }
  });
}

/**
 * Starts an enable/disable job directly - the one sanctioned plan-free
 * mutation path (kind_toggle.go) - and binds it to the control that started
 * it, mirroring confirmPlan's own bindingJob fencing so onJobDone's
 * toast-vs-inline decision can never race a job that finishes before its
 * own start response has even been read.
 *
 * A failure to START (a 409 over the queue-depth cap, a network error) has
 * no modal to render in the way a plan's error state does - unlike every
 * other mutation in this application, a toggle has no Plan step at all - so
 * it becomes a toast instead, the same "the origin isn't on screen to say
 * so" surface every other unseen outcome already uses.
 */
async function startToggle({ action, sourceID, modID, origin }) {
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await startBinding(origin, async () => {
    try {
      const { job_id: jobID } = await startToggleJob(
        action,
        sourceID,
        modID,
        context,
      );
      store.set({ origins: { ...store.get().origins, [origin]: jobID } });
    } catch (err) {
      pushToast({
        tone: "failure",
        title: `${action} failed`,
        detail: err instanceof ApiError ? err.message : String(err),
      });
    }
  });
}

/**
 * refreshAfterModSetting re-reads whatever is on screen after a
 * lock/unlock/update-policy write - a THIN, synchronous mutation
 * (api_mod_settings.go), not a job, so there is no onJobDone re-hydrate to
 * do this automatically the way every job-backed mutation gets for free.
 * Refreshes the library's own mods document when the slide-over (or the
 * plain library table) is what is on screen, and re-runs the full mod
 * page's own hydrate when the edit came from THAT mod's own full page -
 * both are cheap enough to always re-run rather than trying to patch the
 * single field that changed into two different documents by hand.
 */
async function refreshAfterModSetting(sourceID, modID) {
  const route = store.get().route;
  if (
    route.view === "mod" &&
    route.sourceID === sourceID &&
    route.modID === modID
  ) {
    await hydrateModPage(route, { game: route.game, profile: route.profile });
    return;
  }
  await reload("mods", "/api/v1/mods");
}

/** setModLock/clearModLock/setModUpdatePolicy are the slide-over's and the
 * full mod page's editable lock/policy controls - direct API calls (no
 * plan, no job), refreshing whatever is on screen on success and otherwise
 * rejecting with the ApiError the caller renders inline (there is no modal
 * or toast for these - the control that submitted the edit is right there
 * on screen to say what went wrong). */
async function setModLock(sourceID, modID, version) {
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await apiSetModLock(sourceID, modID, version, context);
  await refreshAfterModSetting(sourceID, modID);
}

async function clearModLock(sourceID, modID) {
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await apiClearModLock(sourceID, modID, context);
  await refreshAfterModSetting(sourceID, modID);
}

async function setModUpdatePolicy(sourceID, modID, policy) {
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  await apiSetModUpdatePolicy(sourceID, modID, policy, context);
  await refreshAfterModSetting(sourceID, modID);
}

/** reloadModPageSlice re-fetches one of the full mod page's two
 * supplementary reads (detail/versions) in isolation - the I3 retry
 * affordance the four Mission Control reads already offer, applied to this
 * page's own pair. The primary read (ModFiles) has no slice retry of its
 * own; a failure there is the whole page's fatal state, retried by
 * revisiting the route (actions.reloadModPage). */
async function reloadModPageSlice(key, fetcher) {
  const route = store.get().route;
  if (route.view !== "mod") return;
  const context = { game: route.game, profile: route.profile };
  try {
    const value = await fetcher(route.sourceID, route.modID, context);
    store.set({
      modPage: { ...store.get().modPage, [key]: value, [`${key}Error`]: null },
    });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    store.set({
      modPage: { ...store.get().modPage, [`${key}Error`]: message },
    });
  }
}

/** describe unwraps a rejection into the modal's error/details pair. An
 * ApiError carries the /api/v1 envelope's typed details, which are rendered
 * rather than dropped - they are often the whole answer (which file
 * conflicts, which plan went stale). */
function describe(err) {
  if (err instanceof ApiError)
    return { error: err.message, details: err.details };
  return { error: String(err), details: null };
}

/**
 * Merges patch into the open modal's apply-time options - the "options"
 * member confirmPlan sends POST /api/v1/jobs (issue 331: install's version/file
 * picker is the first renderer that needs to change what Confirm actually
 * submits). A no-op when no modal is open, which happens only if a renderer
 * fires this after the user has already cancelled - nothing left to patch.
 */
function setPlanOptions(patch) {
  const modal = store.get().modal;
  if (!modal) return;
  store.set({
    modal: {
      ...modal,
      applyOptions: { ...(modal.applyOptions ?? {}), ...patch },
    },
  });
}

/** Detaches a finished job from its control, returning it to its idle
 * state. Only ever called for a job that has ENDED - a running job's
 * progress is not dismissible, because hiding a mutation in flight is how a
 * user comes to believe it never happened. */
function clearOrigin(origin) {
  const origins = { ...store.get().origins };
  delete origins[origin];
  store.set({ origins });
}

// succeededOriginReleaseMillis is how long a SUCCEEDED job keeps the control
// it was started from before that control returns to being itself (I-2,
// epic live review).
//
// "The control you clicked morphs into its progress" reads correctly on a
// row or a card, where the outcome simply stays where you left it. On the
// top bar's Deploy it meant the application's primary action was
// unavailable until the user closed a success message: install something
// else, and the way to deploy it was to first dismiss the last deploy's
// "Done". A success has nothing left to say a few seconds later - the tray
// keeps the whole session's record either way - so it hands the control
// back on its own.
//
// FAILURES are deliberately exempt. A failure carries the next step (the
// envelope's own message, and often the affordance that answers it - the
// conflict round trip's Overwrite button lives inside this readout), so it
// waits for the user. Same reason toasts only auto-dismiss on success.
const succeededOriginReleaseMillis = 4000;

/** releaseSucceededOrigin hands origin's control back once the job it is
 * showing has had its few seconds on screen. Guarded on the origin still
 * naming THAT job: a control the user has already started something else
 * from must not be cleared out from under the new job. */
function releaseSucceededOrigin(origin, jobID) {
  setTimeout(() => {
    if (store.get().origins[origin] !== jobID) return;
    clearOrigin(origin);
  }, succeededOriginReleaseMillis);
}

let toastSeq = 0;
const toastDismissMillis = 8000;

/** Pushes a toast. Successes clear themselves after a while; failures do
 * not - a failure nobody saw is the case toasts exist for. */
function pushToast(toast) {
  const id = `t${++toastSeq}`;
  store.set({ toasts: [...store.get().toasts, { ...toast, id }] });
  if (toast.tone !== "failure") {
    setTimeout(() => dismissToast(id), toastDismissMillis);
  }
}

function dismissToast(id) {
  store.set({ toasts: store.get().toasts.filter((t) => t.id !== id) });
}

/**
 * refreshSearchResults re-runs whatever search state is currently cached
 * (M5, unit 5 fix wave): a hit's own `installed` flag is only as fresh as
 * the report that produced it, and this application's search reports are
 * NOT part of hydrate()'s own route-scoped refresh (they can be showing on
 * the home route while hydrate() only re-fetches Mission Control's four
 * documents) - without this, a row that had just been installed through it
 * kept offering "Install" until the next explicit search. Both slices are
 * independent, so either, both, or neither may be populated at call time.
 */
function refreshSearchResults() {
  const { omnibarSearch, searchPage } = store.get();
  if (omnibarSearch) searchSources(omnibarSearch.query);
  if (searchPage)
    runSearchPage({
      query: searchPage.query,
      page: searchPage.page,
      category: searchPage.category,
      source: searchPage.source,
    });
}

/**
 * Handles a job reaching a terminal state, wherever it was started from.
 *
 * Two things follow from any completed mutation. The documents on screen
 * are now stale - a deploy just changed every mod's deployed flag - so the
 * route re-hydrates. And if the control that started it is NOT on screen,
 * the outcome would otherwise be invisible, so it becomes a toast; if it IS
 * on screen it resurfaces there and toasting it too would be telling the
 * user something they are already looking at (design doc §Jobs).
 */
async function onJobDone(summary) {
  // Resolve any startSequencedBatch step waiting on THIS job specifically,
  // before anything else - a batch's next item must be able to start the
  // instant this one is known done, not after the (slower) whole-route
  // re-hydrate below.
  const waiter = jobDoneWaiters.get(summary.id);
  if (waiter) {
    jobDoneWaiters.delete(summary.id);
    waiter(summary);
  }

  // The toast rule is a question about the screen AS THE JOB LANDED, and
  // the next two lines change that screen: refreshSearchResults puts the
  // omnibar's (and the search page's) result list into its loading state,
  // which unmounts the very row this job's outcome is being rendered on.
  // Asking isOriginMounted afterwards therefore got "no" for every install
  // started from a search row, and every one of them toasted on top of the
  // inline outcome the user was already looking at (C-2). The set is
  // snapshotted here, BEFORE anything can unmount, and consulted below once
  // the origin is actually known.
  const mountedAtCompletion = mountedOriginsSnapshot();

  hydrate(store.get().route);
  refreshSearchResults();

  // Wait for every currently in-flight start to bind its origin before
  // deciding: see bindingJobs. The job that just finished could be behind
  // ANY of them, not just the most recently started one.
  await awaitBindings();

  const origin = originOf(summary.id);
  if (origin && summary.state === "succeeded")
    releaseSucceededOrigin(origin, summary.id);

  // Either tense counts as "in view": the snapshot answers for a control
  // this function's own refresh took off screen, and the live check for a
  // control that only MOUNTED once the binding landed (a row rendered by
  // the very refresh above).
  if (origin && (mountedAtCompletion.has(origin) || isOriginMounted(origin)))
    return;

  pushToast(
    summary.state === "failed"
      ? {
          tone: "failure",
          title: `${summary.kind} failed`,
          detail: summary.error?.error ?? "",
          jobID: summary.id,
          ...toastAffordance(summary),
        }
      : {
          tone: "success",
          title: `${summary.kind} finished`,
          jobID: summary.id,
          ...toastAffordance(summary),
        },
  );
}

// jobToastAffordances maps a job KIND to the one place its outcome
// actually lives, for the kinds whose own surface is gone by the time they
// finish (issue 334, the N7 carry).
//
// profile_import is the case: it is started from inside the profiles modal,
// and the confirm-plan modal REPLACES that modal in the shared slot ("modals
// stack at most one deep", design doc §Modals). So the import's own result -
// a profile that now exists, or does not - lands with nothing on screen that
// shows profiles, and the user has to know to go and re-open "Manage
// profiles…" to find out what they just did. Re-opening the modal FOR them
// was the other option the carry named and is the wrong one: it would seize
// the screen from whatever they moved on to, minutes later, for a job they
// may already have forgotten. An offer they can decline is the honest shape.
//
// Keyed by action NAME rather than by function because a toast is store
// state, and the store holds plain data.
const jobToastAffordances = {
  profile_import: { action: "openProfilesModal", actionLabel: "Open profiles" },
};

/** toastAffordance is summary's own extra offer, or nothing. */
function toastAffordance(summary) {
  return jobToastAffordances[summary.kind] ?? {};
}

/** originOf finds which control (if any) started jobID. */
function originOf(jobID) {
  return Object.keys(store.get().origins).find(
    (origin) => store.get().origins[origin] === jobID,
  );
}

/** The actions threaded down to the components: the four supplementary
 * documents' retry/re-run, and the mutation pipeline every mutation in this
 * application goes through (openPlan -> confirmPlan -> a job). */
const actions = {
  reloadMods: () => reload("mods", "/api/v1/mods"),
  reloadUpdates: () => reload("updates", "/api/v1/updates"),
  reloadHealth: () => reload("health", "/api/v1/health"),
  reloadConflicts: () => reload("conflicts", "/api/v1/conflicts"),
  reloadModPage: () => {
    const route = store.get().route;
    if (route.view === "mod")
      hydrateModPage(route, { game: route.game, profile: route.profile });
  },
  reloadModDetail: () => reloadModPageSlice("detail", getModDetail),
  reloadModVersions: () => reloadModPageSlice("versions", getModVersions),
  reloadProfiles: () => reload("profiles", "/api/v1/profiles"),
  reloadStatus,
  openPlan,
  // closeModal is generic over every shape the shared modal slot can hold
  // (store.js: "another shape in this same slot, not another slot") -
  // reorder/profiles/uninstall-batch close through it directly; closePlan
  // is the same function under the name confirmplan.js already calls it by.
  closeModal,
  closePlan: closeModal,
  confirmPlan,
  setPlanOptions,
  replanWith,
  searchSources,
  searchPageGoTo,
  searchPageSetCategory,
  searchPageSetSource,
  startToggle,
  setModLock,
  clearModLock,
  setModUpdatePolicy,
  clearOrigin,
  dismissToast,
  // pushToast is exposed directly (I1, unit 6 fix wave): the row menu's own
  // Lock/Unlock has no control left on screen to show its own error once it
  // closes (unlike modpanel.js's ModSettingsControls, which stays open and
  // renders one inline) - a toast is the only honest place left to put it.
  pushToast,
  retryInstallOverwrite,
  canRetryInstallOverwrite,
  openReorderModal,
  openProfilesModal,
  openShortcutsModal,
  openUninstallBatchModal,
  startBatchToggle,
  startUninstallBatch,
};

// contextKey identifies the data a route needs, not the route itself: the
// ?mod= slide-over annotation (route.mod) is carried on the route object but
// never changes what Mission Control has to fetch (router.js's own doc
// comment: "?mod= annotates the current URL" instead of routing).
// sourceID/modID are one exception - issue 330: the FULL MOD PAGE's view IS
// "which mod", so a direct transition between two mods' full pages (a
// dependency cross-link, say) must re-hydrate even though view/game/profile
// all stayed the same. A search's own ?q= (route.q) is the other (issue 331): the
// SEARCH PAGE's view IS "which query", so navigating from one deep link to
// another (`/search?q=a` -> `/search?q=b`) must re-run the search even
// though view/game/profile stayed the same too - every other route ignores
// q entirely, matching router.js's own doc comment for it.
function contextKey(route) {
  const q = route.view === "search" ? `:${route.q ?? ""}` : "";
  return `${route.view}:${route.game}:${route.profile}:${route.sourceID ?? ""}:${route.modID ?? ""}${q}`;
}

// lastHydratedContext starts undefined, which never equals a real
// contextKey - so the very first go() call always hydrates, including the
// chooser (whose route is otherwise identical to store.js's initial state).
let lastHydratedContext;

// lastGameProfile is contextKey's own game/profile half, tracked separately
// (I5, unit 5 fix wave): omnibarSearch/searchPage/installRequests answer a
// SPECIFIC game+profile, not a route, so they must clear on a switch between
// two HOME routes just as much as on a switch away from one - a case
// contextKey's own comparison (which also folds in view/mod/query, and so
// changes on plenty of same-context navigations that must NOT clear these)
// cannot answer by itself. undefined never equals a real "game:profile"
// pair, so the very first go() call never clears anything there is nothing
// in yet.
let lastGameProfile;

function go(route) {
  const gameProfile = `${route.game}:${route.profile}`;
  if (lastGameProfile !== undefined && gameProfile !== lastGameProfile) {
    // Neither slice is scoped to game/profile on the wire - they are
    // client-only caches of a report that answered a DIFFERENT context's
    // question (which sources, which installed set). Left alone, a stale
    // omnibarSearch/searchPage kept rendering the previous profile's "From
    // sources" rows, with a live Install button, over the new one.
    store.set({ omnibarSearch: null, searchPage: null });
    installRequests.clear();
  }
  lastGameProfile = gameProfile;

  store.set({ route });
  const key = contextKey(route);
  if (key !== lastHydratedContext) {
    lastHydratedContext = key;
    hydrate(route);
  }
}

// The shell's inline script already stamped any persisted override before
// the first paint; re-applying it here is what keeps this module the owner
// of every change after that one (theme.js).
setTheme(currentTheme());

store.subscribe(draw);
onRouteChange(go);
go(parseLocation());

// One session-long connection, opened after the first route is on screen:
// every job this process runs - started here, in another tab, or before
// this page loaded - arrives on it (activity.js).
connectActivity(store, { onJobDone });
