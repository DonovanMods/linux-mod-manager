// topbar.js - Mission Control's top bar: the game and profile pickers, the
// undeployed-changes indicator + Deploy, the omnibar (library live-filter
// only in this unit - source fan-out is Unit 5), the activity bell's
// read-only tray, and the theme toggle
// (docs/plans/2026-08-31-serve-spa-design.md §Mission Control: "Top bar").
//
// The activity bell and its tray live in tray.js: they are the top bar's
// biggest region by far, and the only one with two live streams behind it.
//
// Deploy is this unit's one WIRED mutation, and it is wired through the
// framework every later one uses: it opens the confirm-plan modal, and the
// button itself morphs into the job's progress once confirmed (InlineJob).
// "Manage profiles…" is still present and disabled - it is Unit 6's, and
// the pre-flight forbids forking the framework early to land it sooner.

import { html, useEffect, useRef, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { resolveGamePath } from "../navigation.js";
import { countUndeployed } from "../modrows.js";
import { InlineJob } from "./jobprogress.js";
import { ActivityBell } from "./tray.js";
import { NOT_YET } from "../ui.js";

// DEPLOY_ORIGIN is the key the top bar's Deploy control morphs on. Origins
// are stable strings, one per control (jobprogress.js) - a later unit's
// per-mod controls use "install:fake/123" and friends.
const DEPLOY_ORIGIN = "deploy";

export function TopBar({
  state,
  status,
  games,
  route,
  mods,
  query,
  onQueryChange,
  onThemeChange,
  actions,
}) {
  const undeployed = countUndeployed(mods);

  // Which of the three dropdowns (game/profile/activity) is open, lifted
  // here rather than left as each picker's own useState (M5): opening one
  // must close whatever else was open, and a single outside-click/Escape
  // listener needs one source of truth to close instead of three.
  const [openPicker, setOpenPicker] = useState(null);
  const barRef = useRef(null);

  // ?job={id} opens the tray on that entry - the annotation the deleted
  // /jobs/{id} page's 301 now points at (spa.go). It is an effect rather
  // than an initial state so that arriving at such a URL from INSIDE the
  // application (a toast's "Show in activity") opens the tray too, not just
  // a cold load.
  const deepLinkJob = route.job ?? "";
  useEffect(() => {
    if (deepLinkJob) setOpenPicker("activity");
  }, [deepLinkJob]);

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
      <${InlineJob} origin=${DEPLOY_ORIGIN} state=${state} actions=${actions}>
        <button
          type="button"
          class="button button--primary"
          data-action="deploy"
          onClick=${() =>
            actions.openPlan({
              kind: "deploy",
              origin: DEPLOY_ORIGIN,
              title: "Deploy this profile",
              confirmLabel: "Deploy",
              options: {},
            })}
        >
          Deploy
        </button>
      <//>
      <input
        type="search"
        class="omnibar"
        name="q"
        placeholder="Filter your library…"
        value=${query}
        onInput=${(e) => onQueryChange(e.currentTarget.value)}
      />
      <${ActivityBell}
        state=${state}
        deepLinkJob=${deepLinkJob}
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
