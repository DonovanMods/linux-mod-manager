// components/app.js - the root component.
//
// A skeleton: Unit 1 builds the shell, the router, the store and the theme,
// and proves the whole loop works end to end. Mission Control, the library,
// the slide-over and every modal land in the units after it
// (docs/plans/2026-08-31-webui-impl.md §Units).

import { html } from "../render.js";
import { currentTheme, cycleTheme } from "../theme.js";

/**
 * The one fact Unit 1 renders FROM the hydrated document, so that "the
 * store fetched /api/v1/status" is observable in the DOM and not only in
 * the network tab. It is what the browser E2E asserts on (e2e_test.go): the
 * URL carries the game's ID, this line carries its NAME, which nothing but
 * the fetched document knows. Mission Control replaces the whole component
 * in Unit 2.
 *
 * A scoped route answers core.GameStatus, which embeds the game (so it has
 * a name); the chooser route answers core.StatusReport, which is a list of
 * games and has no name of its own.
 */
function hydratedSummary(status) {
  if (status.name) {
    return `${status.name}: ready.`;
  }
  const games = status.games?.length ?? 0;
  return `${games} game${games === 1 ? "" : "s"} configured.`;
}

/** The application root: reads the route and the hydrated status document. */
export function App({ state, onThemeChange }) {
  const { route, status, error } = state;

  return html`
    <header class="app-bar">
      <h1 class="app-title">lmm</h1>
      <button
        type="button"
        class="theme-toggle"
        onClick=${() => onThemeChange(cycleTheme())}
      >
        Theme: ${currentTheme()}
      </button>
    </header>
    <main class="app-main">
      ${error && html`<p class="app-error">${error}</p>`}
      ${
        route.view === "chooser"
          ? html`<p class="section-header">Choose a game</p>`
          : html`<p class="section-header">${route.game} / ${route.profile}</p>`
      }
      ${
        status === null
          ? html`<p class="app-booting">Loading&#8230;</p>`
          : html`<p class="app-ready" data-hydrated="true">
              ${hydratedSummary(status)}
            </p>`
      }
    </main>
  `;
}
