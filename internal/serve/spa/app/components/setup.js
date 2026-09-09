// setup.js - the Setup page at /g/{game}/{profile}/setup (issue 333, design
// doc §Scope: "a real Setup surface"). A whole page rather than a modal,
// per the task brief - "a Setup page that earns it" - with one section per
// admin surface: Games, Authentication, Custom sources, Archive import,
// Adopt. Reached from the top bar's ⚙ (topbar.js), the empty-library links
// (library.js), or straight after first-run add/detect via a game's own
// Mission Control link.
//
// Each section owns its own data and mutations; this file is only the
// shell - the header (matching searchpage.js/fullmodpage.js's own "leaves
// home" pattern) and the section switcher, seeded from ?section= for a deep
// link (router.js) and otherwise defaulting to Games.

import { html, useEffect, useRef, useState } from "../render.js";
import { navigate, contextPath, setupPath } from "../router.js";
import { currentTheme, cycleTheme } from "../theme.js";
import { SetupGames } from "./setupgames.js";
import { SetupAuth } from "./setupauth.js";
import { SetupSources } from "./setupsources.js";
import { SetupImportArchive } from "./setupimport.js";
import { SetupAdopt } from "./setupadopt.js";

const SECTIONS = [
  { key: "games", label: "Games" },
  { key: "auth", label: "Authentication" },
  { key: "sources", label: "Custom sources" },
  { key: "archive", label: "Archive import" },
  { key: "adopt", label: "Adopt" },
];

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

export function SetupPage({ state, route, onThemeChange, actions }) {
  const [section, setSection] = useState(
    SECTIONS.some((s) => s.key === route.section) ? route.section : "games",
  );
  const tabRefs = useRef([]);

  // A deep link's own ?section= wins even when this page is already
  // mounted (the empty-library links can point here twice in a row with a
  // different section) - hooks run unconditionally, before either early
  // return below, matching missioncontrol.js's own rule.
  useEffect(() => {
    if (SECTIONS.some((s) => s.key === route.section))
      setSection(route.section);
  }, [route.section]);

  // selectSection is every way a tab becomes active: a click, or a
  // keyboard move (selectSection below). It writes ?section= (Minor
  // Minor 13(b) - a reload used to always land back on Games, since setSection
  // alone never touched the URL) with replace: true, the same convention
  // main.js's own chooser redirect uses - a tab switch is not a
  // genuinely new place, just an edit to where this one already is.
  function selectSection(key) {
    setSection(key);
    navigate(setupPath(route.game, route.profile, key), { replace: true });
  }

  // onTabKeyDown implements the WAI-ARIA tabs pattern's arrow-key
  // navigation (Minor 13c): ArrowLeft/ArrowRight move and activate the
  // adjacent tab (wrapping), Home/End jump to the first/last. Automatic
  // activation - moving focus also switches the panel - is what makes
  // sense here: aria-selected already tracks `section`, and a roving
  // tabindex (below) means only the active tab is ever a Tab stop, so a
  // keyboard user's Tab key always lands on it.
  function onTabKeyDown(e, index) {
    let target = -1;
    if (e.key === "ArrowRight") target = (index + 1) % SECTIONS.length;
    else if (e.key === "ArrowLeft")
      target = (index - 1 + SECTIONS.length) % SECTIONS.length;
    else if (e.key === "Home") target = 0;
    else if (e.key === "End") target = SECTIONS.length - 1;
    else return;
    e.preventDefault();
    selectSection(SECTIONS[target].key);
    tabRefs.current[target]?.focus();
  }

  const home = contextPath(route.game, route.profile);
  const header = html`
    <header class="app-bar">
      <span class="app-bar__brand">LMM</span>
      <${BackLink} to=${home} />
      <button
        type="button"
        class="theme-toggle"
        onClick=${() => onThemeChange(cycleTheme())}
      >
        Theme: ${currentTheme()}
      </button>
    </header>
  `;

  if (state.error) {
    return html`
      ${header}
      <main id="main" class="app-main">
        <p class="app-error">${state.error}</p>
      </main>
    `;
  }
  if (state.status === null) {
    return html`${header}
      <p class="app-booting">Loading setup…</p>`;
  }

  return html`
    ${header}
    <main id="main" class="app-main setup-page" data-testid="setup-page">
      <p class="section-header">Setup</p>
      <nav class="setup-nav" role="tablist" aria-label="Setup sections">
        ${SECTIONS.map(
          (s, i) => html`
            <button
              type="button"
              id=${`setup-tab-${s.key}`}
              ref=${(el) => {
                tabRefs.current[i] = el;
              }}
              role="tab"
              aria-selected=${section === s.key ? "true" : "false"}
              aria-controls="setup-panel"
              tabindex=${section === s.key ? "0" : "-1"}
              data-section=${s.key}
              class="setup-nav__tab ${section === s.key ? "setup-nav__tab--active" : ""}"
              onClick=${() => selectSection(s.key)}
              onKeyDown=${(e) => onTabKeyDown(e, i)}
            >
              ${s.label}
            </button>
          `,
        )}
      </nav>
      <div
        id="setup-panel"
        class="setup-page__body"
        role="tabpanel"
        aria-labelledby=${`setup-tab-${section}`}
      >
        ${
          section === "games" &&
          html`<${SetupGames}
            actions=${actions}
            game=${route.game}
            profile=${route.profile}
          />`
        }
        ${section === "auth" && html`<${SetupAuth} />`}
        ${section === "sources" && html`<${SetupSources} />`}
        ${
          section === "archive" &&
          html`<${SetupImportArchive} state=${state} actions=${actions} />`
        }
        ${section === "adopt" && html`<${SetupAdopt} state=${state} actions=${actions} />`}
      </div>
    </main>
  `;
}
