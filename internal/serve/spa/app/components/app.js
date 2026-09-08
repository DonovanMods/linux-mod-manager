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
import { SearchPage } from "./searchpage.js";
import { ConfirmPlanModal } from "./confirmplan.js";
import { ReorderModal } from "./reordermodal.js";
import { ProfilesModal } from "./profilesmodal.js";
import { UninstallBatchModal } from "./uninstallbatchmodal.js";
import { Toasts } from "./toasts.js";

/** The application root: reads the route and dispatches to its screen. The
 * chooser owns its own minimal header (no game/profile context exists yet);
 * Mission Control's top bar carries the theme toggle for every other route. */
export function App({ state, onThemeChange, actions }) {
  const { route, error } = state;

  // The overlays every route carries. Rendered as a Fragment beside the
  // screen rather than inside it, so switching screens cannot take a
  // running job's modal or a pending toast down with it.
  //
  // Each modal is mounted ONLY when the shared slot actually holds its own
  // shape (C1, unit 6 fix wave) - never as a permanent sibling that renders
  // `null` for every other shape. Preact does not discard a component's own
  // hook list on a null render: mounted permanently, ReorderModal and
  // UninstallBatchModal's hooks (called AFTER their own internal
  // `modal?.type !== "..."` guard) kept the PREVIOUS open's state - a stale
  // `entries`/`order` that the next open's fresh `modal` prop never
  // recomputed, which is what let "Cancel" on the reorder modal commit an
  // abandoned edit and let the uninstall batch modal uninstall a mod that
  // was never (re-)selected. Conditionally mounting here means Preact tears
  // the WHOLE component - hook list included - down on close and builds a
  // genuinely fresh one on the next open, no matter what shape it is next
  // time. Each component's own internal guard stays in place as
  // belt-and-braces, not as the only line of defense.
  const modalType = state.modal?.type;
  const overlays = html`
    ${
      modalType === "plan" &&
      html`<${ConfirmPlanModal} modal=${state.modal} actions=${actions} />`
    }
    ${
      modalType === "reorder" &&
      html`<${ReorderModal}
        modal=${state.modal}
        state=${state}
        actions=${actions}
      />`
    }
    ${
      modalType === "profiles" &&
      html`<${ProfilesModal}
        modal=${state.modal}
        state=${state}
        actions=${actions}
      />`
    }
    ${
      modalType === "uninstall-batch" &&
      html`<${UninstallBatchModal}
        modal=${state.modal}
        state=${state}
        actions=${actions}
      />`
    }
    <${Toasts}
      toasts=${state.toasts}
      route=${route}
      onDismiss=${actions.dismissToast}
    />
  `;

  if (route.view === "home") {
    // key forces a fresh MissionControl (and its own local omnibar-text
    // state) on every game/profile switch (I5, unit 5 fix wave): the
    // pickers navigate() rather than reload, which would otherwise keep the
    // SAME instance mounted across the switch, along with a previous
    // profile's omnibar text long after its own fan-out had been cleared by
    // main.js#go.
    return html`
      <${MissionControl}
        key=${`${route.game}:${route.profile}`}
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

  if (route.view === "search") {
    return html`
      <${SearchPage}
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
      <${GameChooser} games=${state.games} />
    </main>
    ${overlays}
  `;
}
