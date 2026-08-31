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
    store.set({ status, games: allStatus.games, error: null });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    store.set({ status: null, games: null, error: message });
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
  const base = { kind, origin, title, confirmLabel, seq };

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
  try {
    const { job_id: jobID } = await startJob(modal.planID);
    if (store.get().modal?.seq !== modal.seq) return;
    store.set({
      modal: null,
      origins: { ...store.get().origins, [modal.origin]: jobID },
    });
  } catch (err) {
    if (store.get().modal?.seq !== modal.seq) return;
    store.set({ modal: { ...modal, status: "error", ...describe(err) } });
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

/** Detaches a finished job from its control, returning it to its idle
 * state. Only ever called for a job that has ENDED - a running job's
 * progress is not dismissible, because hiding a mutation in flight is how a
 * user comes to believe it never happened. */
function clearOrigin(origin) {
  const origins = { ...store.get().origins };
  delete origins[origin];
  store.set({ origins });
}

/** Pushes a toast. Successes clear themselves after a while; failures do
 * not - a failure nobody saw is the case toasts exist for. */
function pushToast(toast) {
  const id = `t${++toastSeq}`;
  store.set({ toasts: [...store.get().toasts, { ...toast, id }] });
  if (toast.tone !== "failure") {
    setTimeout(() => dismissToast(id), toastDismissMillis);
  }
}

let toastSeq = 0;
const toastDismissMillis = 8000;

function dismissToast(id) {
  store.set({ toasts: store.get().toasts.filter((t) => t.id !== id) });
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
function onJobDone(summary) {
  hydrate(store.get().route);

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
  openPlan,
  closePlan: () => {
    modalSeq += 1;
    store.set({ modal: null });
  },
  confirmPlan,
  clearOrigin,
  dismissToast,
};

// contextKey identifies the data a route needs, not the route itself: the
// ?mod= slide-over annotation (route.mod) and a search's ?q= (route.q) are
// both carried on the route object but never change what Mission Control
// has to fetch (router.js's own doc comment: "?mod= annotates the current
// URL" instead of routing). Only view/game/profile decide that.
function contextKey(route) {
  return `${route.view}:${route.game}:${route.profile}`;
}

// lastHydratedContext starts undefined, which never equals a real
// contextKey - so the very first go() call always hydrates, including the
// chooser (whose route is otherwise identical to store.js's initial state).
let lastHydratedContext;

function go(route) {
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
