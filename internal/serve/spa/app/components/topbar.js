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
import { navigate, contextPath, setupPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { resolveGamePath } from "../navigation.js";
import { countUndeployed } from "../modrows.js";
import { InlineJob } from "./jobprogress.js";
import { ActivityBell } from "./tray.js";

// DEPLOY_ORIGIN is the key the top bar's Deploy control morphs on. Origins
// are stable strings, one per control (jobprogress.js) - a later unit's
// per-mod controls use "install:fake/123" and friends.
const DEPLOY_ORIGIN = "deploy";

// SWITCH_ORIGIN is the "Switch and deploy…" control's own origin. ONE
// origin rather than one per profile row: the menu closes the instant a
// switch is confirmed, so no per-row control survives to morph - and a
// switch is not a mutation you can have two of in flight anyway (core's
// beginOp would serialize them, and the second's plan would be stale
// before it started).
//
// It morphs a SLOT of its own in the bar, never the picker (I2, unit 8 gate
// review). The picker used to be wrapped whole in <InlineJob>, so a
// finished switch replaced the application's primary context control with a
// "Done ✕" that did not time out. A button that becomes its own job's
// progress is the design - Deploy does exactly that - but a NAVIGATION
// control that does is a control the user has lost.
const SWITCH_ORIGIN = "switch";

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

  // The profile a "Switch and deploy…" is currently moving to, remembered
  // from the click that started it (I2, unit 8 gate review). core.
  // SwitchResult reports counts, not the target's name, and the switch job
  // outlives the dropdown that named it - so the one place that still knows
  // is the control that asked. A ref rather than state: nothing renders
  // from it, and a re-render mid-job must not be able to lose it.
  const pendingSwitch = useRef(null);
  const switchJobID = state.origins?.[SWITCH_ORIGIN];
  const switchState = (state.jobsIndex ?? []).find(
    (row) => row.id === switchJobID,
  )?.state;

  // When the switch lands, the screen follows the machine. Without this the
  // route, the library and the deploy indicator all went on describing the
  // profile the user had just switched away from - the indicator reading
  // "1 change undeployed" for a profile whose Deploy plan says there is
  // nothing to deploy, two statements about the same profile contradicting
  // each other on the same screen.
  //
  // Navigating remounts Mission Control (app.js keys it on game:profile),
  // so this effect's own ref starts empty on the other side and cannot
  // navigate twice for one job.
  useEffect(() => {
    if (switchState !== "succeeded") return;
    const to = pendingSwitch.current;
    pendingSwitch.current = null;
    if (!to || to === route.profile) return;
    navigate(contextPath(route.game, to));
  }, [switchJobID, switchState, route.game, route.profile]);

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
      if (e.key !== "Escape") return;
      // Focus goes back to the trigger that owns this dropdown (issue 334's
      // a11y pass). Without it Escape closes the menu and leaves the
      // keyboard nowhere - the focused menu item has just been removed from
      // the document, so the next Tab restarts from the top of the page.
      // Queried fresh here rather than captured at open: the trigger is
      // re-rendered while the menu is up (its own aria-expanded changes).
      const trigger = barRef.current?.querySelector(
        `[data-picker="${openPicker}"]`,
      );
      setOpenPicker(null);
      if (trigger instanceof HTMLElement) trigger.focus();
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
        onSwitchRequested=${(name) => {
          pendingSwitch.current = name;
        }}
        actions=${actions}
      />
      <span class="app-bar__switch">
        <${InlineJob}
          origin=${SWITCH_ORIGIN}
          state=${state}
          actions=${actions}
        ><//>
      </span>
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
        placeholder="Filter your library, or press Enter to search sources…"
        value=${query}
        onInput=${(e) => onQueryChange(e.currentTarget.value)}
        onKeyDown=${(e) => {
          if (e.key === "Enter") actions.searchSources(query);
        }}
      />
      ${
        query.trim() &&
        html`
          <button
            type="button"
            class="button button--small omnibar__fanout"
            onClick=${() => actions.searchSources(query)}
          >
            search sources ↵
          </button>
        `
      }
      <${ActivityBell}
        state=${state}
        deepLinkJob=${deepLinkJob}
        open=${openPicker === "activity"}
        onOpen=${() => setOpenPicker("activity")}
        onClose=${() => setOpenPicker(null)}
        actions=${actions}
      />
      <button
        type="button"
        class="button button--small"
        data-action="setup"
        title="Setup"
        aria-label="Setup"
        onClick=${() => navigate(setupPath(route.game, route.profile))}
      >
        ⚙ Setup
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
        data-picker="game"
        aria-haspopup="true"
        aria-expanded=${open ? "true" : "false"}
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
 * known without another round trip.
 *
 * That plain navigation is only ever a VIEW change: it moves what this
 * browser is looking at and touches nothing on disk. The real `lmm profile
 * switch` - undeploy what the old profile deployed, deploy what the new one
 * lists, and move the game's active profile - is the row's own "Switch and
 * deploy…" (issue 334), which goes through the confirm-plan framework like
 * every other mutation. Keeping both is deliberate: the design's own
 * "quick path inline, full path one click away" reads here as "looking is
 * free, changing the machine is confirmed".
 *
 * The affordance is offered for every profile that is not ALREADY the
 * active one (ProfileSummary.is_default, core.GameStatus's own answer to
 * "which profile does this game deploy"): switching to the active profile
 * is the plan's own AlreadyActive case, with nothing to move.
 */
function ProfilePicker({
  status,
  route,
  open,
  onOpen,
  onClose,
  onSwitchRequested,
  actions,
}) {
  const profiles = status.profiles ?? [];

  function pick(name) {
    onClose();
    if (name === route.profile) return;
    navigate(contextPath(route.game, name));
  }

  function switchTo(name) {
    onClose();
    // Told BEFORE the plan opens rather than on Confirm: the modal can be
    // cancelled, and a target remembered for a switch that never started is
    // harmless (nothing reads it until a switch job actually succeeds),
    // while a target recorded only on Confirm would have to be threaded
    // through the modal's own callbacks to get there.
    onSwitchRequested?.(name);
    actions.openPlan({
      kind: "switch",
      origin: SWITCH_ORIGIN,
      title: `Switch to ${name}`,
      confirmLabel: "Switch and deploy",
      options: { profile: name },
      // The control that opened this modal is a menu item inside a
      // dropdown that has just closed, so there is nothing left to return
      // focus to - the picker's own trigger is the stable survivor
      // (modal.js's openerSelector, the I2 pattern the profiles modal
      // already uses).
      openerSelector: ".profile-picker__trigger",
    });
  }

  return html`
    <div class="picker profile-picker">
      <button
        type="button"
        class="picker__trigger profile-picker__trigger"
        data-picker="profile"
        data-profile=${route.profile}
        aria-haspopup="true"
        aria-expanded=${open ? "true" : "false"}
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
                <li key=${p.name} class="picker__row" data-profile=${p.name}>
                  <button
                    type="button"
                    class="picker__item"
                    onClick=${() => pick(p.name)}
                  >
                    ${p.name}
                  </button>
                  ${
                    !p.is_default &&
                    html`<button
                      type="button"
                      class="picker__action"
                      data-action="switch"
                      data-profile=${p.name}
                      onClick=${() => switchTo(p.name)}
                    >
                      Switch and deploy…
                    </button>`
                  }
                </li>
              `,
            )}
            <li class="picker__divider" role="separator"></li>
            <li>
              <button
                type="button"
                class="picker__item"
                onClick=${() => {
                  onClose();
                  actions.openProfilesModal();
                }}
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
