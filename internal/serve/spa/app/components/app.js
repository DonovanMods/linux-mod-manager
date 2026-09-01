// components/app.js - the root component: routes to the game chooser or
// Mission Control, and carries the three things every route needs before
// any of them has anything to say - the theme toggle, the one modal, and
// the toasts.
//
// The modal and the toasts are mounted HERE rather than inside Mission
// Control on purpose. A job outlives the screen that started it: navigate
// to a mod page mid-deploy and the completion still has to find you, which
// it cannot do from a component that just unmounted.

import { html } from "../render.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { GameChooser } from "./gamechooser.js";
import { MissionControl } from "./missioncontrol.js";
import { FullModPage } from "./fullmodpage.js";
import { ConfirmPlanModal } from "./confirmplan.js";
import { Toasts } from "./toasts.js";

/** ComingSoon answers the one route this unit's screens still don't render -
 * the dedicated search page (Unit 5) - so a direct link into it is a real
 * page rather than a blank one. */
function ComingSoon({ route }) {
  const label = route.view === "search" ? "Search" : "This page";
  return html`<p class="app-booting">${label} lands in a later unit.</p>`;
}

/** The application root: reads the route and dispatches to its screen. The
 * chooser owns its own minimal header (no game/profile context exists yet);
 * Mission Control's top bar carries the theme toggle for every other route. */
export function App({ state, onThemeChange, actions }) {
  const { route, error } = state;

  // The overlays every route carries. Rendered as a Fragment beside the
  // screen rather than inside it, so switching screens cannot take a
  // running job's modal or a pending toast down with it.
  const overlays = html`
    <${ConfirmPlanModal} modal=${state.modal} actions=${actions} />
    <${Toasts}
      toasts=${state.toasts}
      route=${route}
      onDismiss=${actions.dismissToast}
    />
  `;

  if (route.view === "home") {
    return html`
      <${MissionControl}
        state=${state}
        onThemeChange=${onThemeChange}
        actions=${actions}
      />
      ${overlays}
    `;
  }

  if (route.view === "mod") {
    return html`
      <${FullModPage}
        state=${state}
        route=${route}
        onThemeChange=${onThemeChange}
        actions=${actions}
      />
      ${overlays}
    `;
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
    ${overlays}
  `;
}
