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
import { connectActivity, isOriginMounted } from "./activity.js";

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
  if (route.view === "chooser") {
    try {
      const status = await get("/api/v1/status");
      store.set({ status, games: status.games, error: null });
    } catch (err) {
      const message = err instanceof ApiError ? err.message : String(err);
      store.set({ status: null, games: null, error: message });
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
    store.set({
      status,
      games: allStatus.games,
      error: null,
      fetchErrors: { ...store.get().fetchErrors, status: null },
    });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    // A RE-hydrate (main.js's onJobDone runs this on every job completion,
    // not just a route change) that fails must not blank a page that
    // already has a status - that would take the inline outcome and the
    // tray down with it over a fetch that has nothing to do with either.
    // The fatal `error` slice is reserved for the FIRST load, where there
    // is nothing on screen yet to protect (the I3 rule, applied here).
    if (store.get().status) {
      store.set({
        fetchErrors: { ...store.get().fetchErrors, status: message },
      });
      return;
    }
    store.set({ status: null, games: null, error: message });
    return;
  }

  if (route.view === "mod") {
    await hydrateModPage(route, context);
    return;
  }
  if (route.view === "search") {
    await runSearchPage(route.q ?? "", 0);
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
  store.set({
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
async function hydrateModPage(route, context) {
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
  const [detail, versions, updates] = await Promise.allSettled([
    getModDetail(route.sourceID, route.modID, context),
    getModVersions(route.sourceID, route.modID, context),
    get(scoped("/api/v1/updates", context)),
  ]);
  if (store.get().modPage?.key !== key) return;
  store.set({
    modPage: {
      ...store.get().modPage,
      detail: settled(detail),
      detailError: failureMessage(detail),
      versions: settled(versions),
      versionsError: failureMessage(versions),
      updates: settled(updates),
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
  const context = {
    game: store.get().route.game,
    profile: store.get().route.profile,
  };
  try {
    const value = await get(scoped(path, context));
    store.set({
      [key]: value,
      fetchErrors: { ...store.get().fetchErrors, [key]: null },
    });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    store.set({
      fetchErrors: { ...store.get().fetchErrors, [key]: message },
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

/**
 * runSearchPage loads one page of the dedicated /search route (design doc
 * §Search: "a dedicated search page ... pagination"). Called by hydrate()
 * on route entry/deep link and by the page's own Next/Prev controls -
 * neither touches the URL's ?q=, which stays the query alone (pagination is
 * client-driven state, not part of the route).
 */
async function runSearchPage(query, page) {
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
  store.set({
    searchPage: {
      status: "loading",
      query: q,
      page,
      pageSize: SEARCH_PAGE_SIZE,
      report: null,
      error: null,
    },
  });
  try {
    const report = await apiSearch(
      q,
      { page, pageSize: SEARCH_PAGE_SIZE },
      context,
    );
    if (searchPageSeq !== seq) return;
    store.set({
      searchPage: {
        status: "ready",
        query: q,
        page,
        pageSize: SEARCH_PAGE_SIZE,
        report,
        error: null,
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
        report: null,
        error: err instanceof ApiError ? err.message : String(err),
      },
    });
  }
}

/** searchPageGoTo re-runs the search page at a different page, for the
 * current query - the Next/Prev controls' own action. */
function searchPageGoTo(page) {
  const current = store.get().searchPage;
  if (!current) return;
  runSearchPage(current.query, page);
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
async function openPlan({ kind, origin, title, confirmLabel, options }) {
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
  const base = { kind, origin, title, confirmLabel, seq, options };

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
      if (modal.kind === "install") {
        rememberInstallRequest(modal.origin, modal.options, modal.applyOptions);
      }
      store.set({
        modal: null,
        origins: { ...store.get().origins, [modal.origin]: jobID },
      });
    } catch (err) {
      if (store.get().modal?.seq !== modal.seq) return;
      store.set({ modal: { ...modal, status: "error", ...describe(err) } });
    }
  });
}

// installRequests remembers the exact (plan-time, apply-time) request pair
// behind each install origin that has successfully STARTED a job - the
// conflict round trip's (failures.js/tray.js) only source for what to
// re-plan once that job fails with *core.ConflictError: the failed job's own
// summary carries the typed envelope, never the request that produced it
// (activity.go's jobSummary has no such field - it is the tray's job to
// offer the next step, not the registry's to remember why). Overwritten on
// every (re-)start of the same origin, so a retry-after-retry always answers
// from the MOST RECENT attempt.
const installRequests = new Map();

function rememberInstallRequest(origin, planOptions, applyOptions) {
  installRequests.set(origin, { planOptions, applyOptions });
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
      const response = await planMutation("install", req.planOptions, context);
      const { job_id: newJobID } = await startJob(response.plan_id, {
        ...req.applyOptions,
        accept_conflicts: true,
      });
      rememberInstallRequest(origin, req.planOptions, req.applyOptions);
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
  if (searchPage) runSearchPage(searchPage.query, searchPage.page);
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
  hydrate(store.get().route);
  refreshSearchResults();

  // Wait for every currently in-flight start to bind its origin before
  // deciding: see bindingJobs. The job that just finished could be behind
  // ANY of them, not just the most recently started one.
  await awaitBindings();

  const origin = originOf(summary.id);
  if (origin && isOriginMounted(origin)) return;

  pushToast(
    summary.state === "failed"
      ? {
          tone: "failure",
          title: `${summary.kind} failed`,
          detail: summary.error?.error ?? "",
          jobID: summary.id,
        }
      : {
          tone: "success",
          title: `${summary.kind} finished`,
          jobID: summary.id,
        },
  );
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
  openPlan,
  closePlan: () => {
    modalSeq += 1;
    store.set({ modal: null });
  },
  confirmPlan,
  setPlanOptions,
  searchSources,
  searchPageGoTo,
  startToggle,
  setModLock,
  clearModLock,
  setModUpdatePolicy,
  clearOrigin,
  dismissToast,
  retryInstallOverwrite,
  canRetryInstallOverwrite,
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
