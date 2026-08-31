// components/app.js - the root component: routes to the game chooser or
// Mission Control, and carries the one thing every route needs before
// either has anything to say - the theme toggle.

import { html } from "../render.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { GameChooser } from "./gamechooser.js";
import { MissionControl } from "./missioncontrol.js";

/** ComingSoon answers the two routes this unit's screens don't render yet -
 * the full mod page and the search page (Units 4 and 5) - so a direct link
 * into either is a real page rather than a blank one. */
function ComingSoon({ route }) {
  const label = route.view === "search" ? "Search" : "The full mod page";
  return html`<p class="app-booting">${label} lands in a later unit.</p>`;
}

/** The application root: reads the route and dispatches to its screen. The
 * chooser owns its own minimal header (no game/profile context exists yet);
 * Mission Control's top bar carries the theme toggle for every other route. */
export function App({ state, onThemeChange }) {
  const { route, error } = state;

  if (route.view === "home") {
    return html`<${MissionControl}
      state=${state}
      onThemeChange=${onThemeChange}
    />`;
  }

  return html`
    <header class="app-bar app-bar--minimal">
      <span class="app-bar__brand">LMM</span>
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
          ? html`<${GameChooser} games=${state.games} />`
          : html`<${ComingSoon} route=${route} />`
      }
    </main>
  `;
}
