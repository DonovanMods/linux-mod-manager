// missioncontrol.js - the home route: the top bar, the attention cards, the
// library, and the ?mod= slide-over placeholder, composed
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control).

import { html, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { TopBar } from "./topbar.js";
import { AttentionCards } from "./cards.js";
import { Library } from "./library.js";
import { ModPanel } from "./modpanel.js";

/** goToChooser is the error state's escape hatch: a click, not just advice
 * to edit the URL bar, back to a context that does resolve. */
function goToChooser(e) {
  e.preventDefault();
  navigate("/");
}

export function MissionControl({ state, onThemeChange, actions }) {
  // Hooks run unconditionally, before either early return below - the
  // query the omnibar edits lives here, one level above the top bar (which
  // renders the input) and the library (which filters by it), rather than
  // in either alone.
  const [query, setQuery] = useState("");

  const {
    route,
    status,
    games,
    mods,
    updates,
    health,
    conflicts,
    jobsIndex,
    error,
    fetchErrors,
  } = state;

  if (error) {
    return html`
      <header class="app-bar app-bar--minimal">
        <span class="app-bar__brand">LMM</span>
      </header>
      <main class="app-main">
        <p class="app-error">${error}</p>
        <p><a href="/" onClick=${goToChooser}>Choose a different game</a></p>
      </main>
    `;
  }

  if (status === null) {
    return html`<p class="app-booting">Loading&#8230;</p>`;
  }

  return html`
    <div class="mission-control" data-hydrated="true">
      <${TopBar}
        status=${status}
        games=${games}
        route=${route}
        mods=${mods?.mods}
        jobsIndex=${jobsIndex}
        query=${query}
        onQueryChange=${setQuery}
        onThemeChange=${onThemeChange}
      />
      <main class="mission-control__body">
        <${AttentionCards}
          updates=${updates}
          health=${health}
          conflicts=${conflicts}
          errors=${fetchErrors}
          actions=${actions}
        />
        <${Library}
          mods=${mods}
          updates=${updates}
          health=${health}
          conflicts=${conflicts}
          query=${query}
          error=${fetchErrors?.mods}
          onRetry=${actions.reloadMods}
        />
      </main>
      ${
        route.mod &&
        html`<${ModPanel}
          modKey=${route.mod}
          contextPath=${contextPath(route.game, route.profile)}
        />`
      }
    </div>
  `;
}
