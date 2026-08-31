// topbar.js - Mission Control's top bar: the game and profile pickers, the
// undeployed-changes indicator + Deploy, the omnibar (library live-filter
// only in this unit - source fan-out is Unit 5), the activity bell's
// read-only tray, and the theme toggle
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Top bar").
//
// Every mutation affordance here - Deploy, "Manage profiles…" - is present
// and disabled: Unit 3 lands the confirm-modal framework they submit
// through, and the pre-flight forbids forking it early.

import { html, useEffect, useRef, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { resolveGamePath } from "../navigation.js";
import { countUndeployed } from "../modrows.js";
import { NOT_YET } from "../ui.js";

export function TopBar({
  status,
  games,
  route,
  mods,
  jobsIndex,
  query,
  onQueryChange,
  onThemeChange,
}) {
  const undeployed = countUndeployed(mods);

  // Which of the three dropdowns (game/profile/activity) is open, lifted
  // here rather than left as each picker's own useState (M5): opening one
  // must close whatever else was open, and a single outside-click/Escape
  // listener needs one source of truth to close instead of three.
  const [openPicker, setOpenPicker] = useState(null);
  const barRef = useRef(null);

  useEffect(() => {
    if (openPicker === null) return;
    function handlePointerDown(e) {
      if (barRef.current && !barRef.current.contains(e.target)) {
        setOpenPicker(null);
      }
    }
    function handleKeyDown(e) {
      if (e.key === "Escape") setOpenPicker(null);
    }
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [openPicker]);

  return html`
    <header class="app-bar" ref=${barRef}>
      <span class="app-bar__brand">LMM</span>
      <${GamePicker}
        status=${status}
        games=${games}
        open=${openPicker === "game"}
        onOpen=${() => setOpenPicker("game")}
        onClose=${() => setOpenPicker(null)}
      />
      <${ProfilePicker}
        status=${status}
        route=${route}
        open=${openPicker === "profile"}
        onOpen=${() => setOpenPicker("profile")}
        onClose=${() => setOpenPicker(null)}
      />
      <span
        class="deploy-indicator ${undeployed > 0 ? "deploy-indicator--pending" : ""}"
      >
        ${
          undeployed > 0
            ? `${undeployed} change${undeployed === 1 ? "" : "s"} undeployed`
            : "Deployed"
        }
      </span>
      <button
        type="button"
        class="button button--primary"
        disabled
        title=${NOT_YET}
      >
        Deploy
      </button>
      <input
        type="search"
        class="omnibar"
        name="q"
        placeholder="Filter your library…"
        value=${query}
        onInput=${(e) => onQueryChange(e.currentTarget.value)}
      />
      <${ActivityBell}
        jobsIndex=${jobsIndex}
        open=${openPicker === "activity"}
        onOpen=${() => setOpenPicker("activity")}
        onClose=${() => setOpenPicker(null)}
      />
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

/** GamePicker switches games via resolveGamePath - the same active-profile
 * resolution the chooser's auto-redirect uses, so a pick always lands on a
 * real Mission Control route rather than a context that can't resolve. */
function GamePicker({ status, games, open, onOpen, onClose }) {
  const list = games ?? [];

  async function pick(id) {
    onClose();
    if (id === status.id) return;
    const path = await resolveGamePath(id).catch(() => null);
    if (path) navigate(path);
  }

  return html`
    <div class="picker game-picker">
      <button
        type="button"
        class="picker__trigger game-picker__trigger"
        onClick=${() => (open ? onClose() : onOpen())}
      >
        ${status.name} ▾
      </button>
      ${
        open &&
        html`
          <ul class="picker__menu game-picker__menu">
            ${list.map(
              (g) => html`
                <li key=${g.id}>
                  <button
                    type="button"
                    class="picker__item"
                    onClick=${() => pick(g.id)}
                  >
                    ${g.name}
                  </button>
                </li>
              `,
            )}
          </ul>
        `
      }
    </div>
  `;
}

/** ProfilePicker switches profiles within the CURRENT game - a plain route
 * change, since the profile's own name (unlike a game switch) is already
 * known without another round trip. */
function ProfilePicker({ status, route, open, onOpen, onClose }) {
  const profiles = status.profiles ?? [];

  function pick(name) {
    onClose();
    if (name === route.profile) return;
    navigate(contextPath(route.game, name));
  }

  return html`
    <div class="picker profile-picker">
      <button
        type="button"
        class="picker__trigger profile-picker__trigger"
        onClick=${() => (open ? onClose() : onOpen())}
      >
        ${route.profile} ▾
      </button>
      ${
        open &&
        html`
          <ul class="picker__menu profile-picker__menu">
            ${profiles.map(
              (p) => html`
                <li key=${p.name}>
                  <button
                    type="button"
                    class="picker__item"
                    onClick=${() => pick(p.name)}
                  >
                    ${p.name}
                  </button>
                </li>
              `,
            )}
            <li class="picker__divider" role="separator"></li>
            <li>
              <button
                type="button"
                class="picker__item"
                disabled
                title=${NOT_YET}
              >
                Manage profiles…
              </button>
            </li>
          </ul>
        `
      }
    </div>
  `;
}

/** ActivityBell reads GET /api/v1/jobs's retained jobs; the tray it opens is
 * read-only in this unit - Unit 3 wires the live SSE stream and the tray's
 * own next-step affordances (e.g. a failed install's "Overwrite?"). */
function ActivityBell({ jobsIndex, open, onOpen, onClose }) {
  const jobs = jobsIndex ?? [];
  const running = jobs.filter((j) => j.state === "running").length;

  return html`
    <div class="picker activity-bell">
      <button
        type="button"
        class="picker__trigger activity-bell__trigger"
        onClick=${() => (open ? onClose() : onOpen())}
      >
        🔔${running > 0 && html`<span class="activity-bell__count">${running}</span>`}
      </button>
      ${
        open &&
        html`
          <ul class="picker__menu tray">
            ${
              jobs.length === 0
                ? html`<li class="tray__empty">No activity yet.</li>`
                : jobs.map(
                    (j) => html`
                      <li key=${j.id} class="tray__row">
                        <span class="tray__kind">${j.kind}</span>
                        <span class="tray__state tray__state--${j.state}"
                          >${j.state}</span
                        >
                      </li>
                    `,
                  )
            }
          </ul>
        `
      }
    </div>
  `;
}
