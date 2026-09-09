// addmodsmenu.js - the "Add mods ▾" control (issue 339, owner Demo 3): once
// a library holds mods, the only visible way to add another was the omnibar
// - Archive import and Adopt lived only under ⚙ Setup, with nothing in a
// populated library pointing at either. This is the ONE component rendered
// in both places library.js needs it (the populated library's toolbar and
// the empty-library state) - the same three entries, landing on the design's
// EXISTING flows rather than a fourth: the omnibar's own fan-out, Setup →
// Archive import, Setup → Adopt.
//
// A plain dropdown, the same shape topbar.js's game/profile pickers use
// (.picker/.picker__trigger/.picker__menu/.picker__item) - reused rather
// than invented, and dismissed the same way (dismiss.js's outside-click/
// Escape rule) since this is one more "click elsewhere to close it" menu
// among several the application already has.

import { html, useRef, useState } from "../render.js";
import { navigate, setupPath } from "../router.js";
import { useDismissOnOutsideOrEscape } from "../dismiss.js";

/** focusOmnibar hands the keyboard to the top bar's own search field - the
 * SAME input the "search sources ↵" fan-out already reads from
 * (topbar.js) - so "Search sources…" here is a shortcut into that flow,
 * never a second one. */
function focusOmnibar() {
  const input = document.querySelector(".omnibar");
  if (input instanceof HTMLElement) input.focus();
}

export function AddModsMenu({ route, actions }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);

  useDismissOnOutsideOrEscape(ref, open ? "add-mods" : null, () =>
    setOpen(false),
  );

  function pick(run) {
    setOpen(false);
    run();
  }

  return html`
    <div class="picker add-mods-menu" ref=${ref}>
      <button
        type="button"
        class="picker__trigger add-mods-menu__trigger"
        data-picker="add-mods"
        data-action="add-mods"
        aria-haspopup="true"
        aria-expanded=${open ? "true" : "false"}
        onClick=${() => setOpen((v) => !v)}
      >
        Add mods ▾
      </button>
      ${
        open &&
        html`
          <ul class="picker__menu add-mods-menu__menu">
            <li>
              <button
                type="button"
                class="picker__item"
                onClick=${() => pick(focusOmnibar)}
              >
                Search sources…
              </button>
            </li>
            <li>
              <button
                type="button"
                class="picker__item"
                onClick=${() =>
                  pick(() =>
                    navigate(setupPath(route.game, route.profile, "archive")),
                  )}
              >
                Import an archive…
              </button>
            </li>
            <li>
              <button
                type="button"
                class="picker__item"
                onClick=${() =>
                  pick(() =>
                    navigate(setupPath(route.game, route.profile, "adopt")),
                  )}
              >
                Adopt untracked mods…
              </button>
            </li>
          </ul>
        `
      }
    </div>
  `;
}
