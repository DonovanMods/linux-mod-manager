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

import { html, useEffect, useState } from "../render.js";
import { navigate, contextPath } from "../router.js";
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

  // A deep link's own ?section= wins even when this page is already
  // mounted (the empty-library links can point here twice in a row with a
  // different section) - hooks run unconditionally, before either early
  // return below, matching missioncontrol.js's own rule.
  useEffect(() => {
    if (SECTIONS.some((s) => s.key === route.section))
      setSection(route.section);
  }, [route.section]);

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
      <main class="app-main">
        <p class="app-error">${state.error}</p>
      </main>
    `;
  }
  if (state.status === null) {
    return html`${header}
      <p class="app-booting">Loading&#8230;</p>`;
  }

  return html`
    ${header}
    <main class="app-main setup-page" data-testid="setup-page">
      <p class="section-header">Setup</p>
      <nav class="setup-nav" role="tablist" aria-label="Setup sections">
        ${SECTIONS.map(
          (s) => html`
            <button
              type="button"
              role="tab"
              aria-selected=${section === s.key ? "true" : "false"}
              data-section=${s.key}
              class="setup-nav__tab ${section === s.key ? "setup-nav__tab--active" : ""}"
              onClick=${() => setSection(s.key)}
            >
              ${s.label}
            </button>
          `,
        )}
      </nav>
      <div class="setup-page__body" role="tabpanel">
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
