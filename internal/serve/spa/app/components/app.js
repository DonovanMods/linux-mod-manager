// components/app.js - the root component.
//
// A skeleton: Unit 1 builds the shell, the router, the store and the theme,
// and proves the whole loop works end to end. Mission Control, the library,
// the slide-over and every modal land in the units after it
// (docs/plans/2026-08-31-webui-impl.md §Units).

import { html } from "../render.js";
import { currentTheme, cycleTheme } from "../theme.js";

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
      ${route.view === "chooser"
        ? html`<p class="section-header">Choose a game</p>`
        : html`<p class="section-header">${route.game} / ${route.profile}</p>`}
      ${status === null
        ? html`<p class="app-booting">Loading&#8230;</p>`
        : html`<p class="app-ready" data-hydrated="true">Ready.</p>`}
    </main>
  `;
}
