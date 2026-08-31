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
import { get, scoped, ApiError } from "./api.js";
import { resolveGamePath } from "./navigation.js";

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
    html`<${App} state=${store.get()} onThemeChange=${() => draw()} />`,
    root,
  );
}

/** Unwraps a Promise.allSettled result, or null for a rejected one - a
 * failure in any ONE of Mission Control's four supplementary documents must
 * not blank the three that loaded fine. */
function settled(result) {
  return result.status === "fulfilled" ? result.value : null;
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

  const [mods, updates, health, conflicts, jobs] = await Promise.allSettled([
    get(scoped("/api/v1/mods", context)),
    get(scoped("/api/v1/updates", context)),
    get(scoped("/api/v1/health", context)),
    get(scoped("/api/v1/conflicts", context)),
    get("/api/v1/jobs"),
  ]);
  store.set({
    mods: settled(mods),
    updates: settled(updates),
    health: settled(health),
    conflicts: settled(conflicts),
    jobsIndex: settled(jobs)?.jobs ?? null,
  });
}

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
