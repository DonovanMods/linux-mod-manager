// awaybar.js - the frame the three routes that LEAVE home still carry: the
// Setup page, the full mod page and the search page (I-6, epic live
// review).
//
// Each of those three had hand-rolled its own header - brand, "← Back to
// library", theme - and between them they dropped the whole frame: no
// activity bell, no keyboard help. Start a long archive import from Setup
// and there was no tray to watch it in; a deploy running in another tab was
// invisible there. Those are FRAME concerns, not home concerns, so they
// belong to every route.
//
// What deliberately does NOT come along is the home half: the game and
// profile pickers, the deploy indicator and Deploy, and the omnibar. Every
// one of them navigates INTO a game+profile context, which is the one thing
// these three routes are a step away from - a game picker on the full mod
// page would offer to switch to a game in which this mod is not installed,
// and land on a page about nothing. "Back to library" is the honest control
// for that, and it is already here.

import { html, useCallback, useRef, useState } from "../render.js";
import { navigate } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { useDismissOnOutsideOrEscape } from "../dismiss.js";
import { ActivityBell } from "./tray.js";

/** BackLink is the one navigation control an away-from-home route keeps. */
function BackLink({ to }) {
  return html`<a
    class="mod-page__back"
    href=${to}
    onClick=${(e) => {
      e.preventDefault();
      navigate(to);
    }}
    >← Back to library</a
  >`;
}

/**
 * AwayBar renders the away-from-home header.
 *
 * `home` is where "← Back to library" goes. `children` is whatever that
 * route wants between the back link and the frame controls (nothing, so
 * far) - the parameter exists so a route with a page-specific control has
 * somewhere to put it that is not a fourth copy of this bar.
 */
export function AwayBar({ state, route, home, onThemeChange, actions }) {
  const [openPicker, setOpenPicker] = useState(null);
  const barRef = useRef(null);
  useDismissOnOutsideOrEscape(
    barRef,
    openPicker,
    useCallback(() => setOpenPicker(null), []),
  );

  return html`
    <header class="app-bar app-bar--away" ref=${barRef}>
      <span class="app-bar__brand">LMM</span>
      <nav class="app-bar__nav" aria-label="Back to library">
        <${BackLink} to=${home} />
      </nav>
      <${ActivityBell}
        state=${state}
        deepLinkJob=${route?.job ?? ""}
        open=${openPicker === "activity"}
        onOpen=${() => setOpenPicker("activity")}
        onClose=${() => setOpenPicker(null)}
        actions=${actions}
      />
      <button
        type="button"
        class="button button--small"
        data-action="shortcuts"
        title="Keyboard shortcuts"
        aria-label="Keyboard shortcuts"
        onClick=${() => actions.openShortcutsModal()}
      >
        ?
      </button>
      <button
        type="button"
        class="theme-toggle"
        onClick=${() => onThemeChange(cycleTheme())}
      >
        Theme: ${currentTheme()}
      </button>
    </header>
  `;
}
