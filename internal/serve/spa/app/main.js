// main.js - the SPA entry point the shell loads as a module.
//
// It wires the four things Unit 1 delivers - the router, the store, the
// theme and the API client - into one render loop, and mounts the root
// component over the shell's placeholder.

import { render } from "./render.js";
import { html } from "./render.js";
import { App } from "./components/app.js";
import { createStore } from "./store.js";
import { parseLocation, onRouteChange } from "./router.js";
import { currentTheme, setTheme } from "./theme.js";
import { get, scoped, ApiError } from "./api.js";

const store = createStore();
const root = document.getElementById("app");

function draw() {
  render(
    html`<${App} state=${store.get()} onThemeChange=${() => draw()} />`,
    root,
  );
}

/**
 * Loads the documents the current route needs. Unit 1 hydrates the status
 * dashboard only; each later unit adds the queries its own screen reads.
 *
 * A failed load is put in the store rather than thrown: the shell is
 * already on screen, and an unreachable endpoint should say so in place,
 * not blank the page.
 */
async function hydrate(route) {
  try {
    const context =
      route.view === "chooser"
        ? undefined
        : { game: route.game, profile: route.profile };
    const status = await get(scoped("/api/v1/status", context));
    store.set({ status, error: null });
  } catch (err) {
    const message = err instanceof ApiError ? err.message : String(err);
    store.set({ status: null, error: message });
  }
}

function go(route) {
  store.set({ route });
  hydrate(route);
}

// The shell's inline script already stamped any persisted override before
// the first paint; re-applying it here is what keeps this module the owner
// of every change after that one (theme.js).
setTheme(currentTheme());

store.subscribe(draw);
onRouteChange(go);
go(parseLocation());
draw();
