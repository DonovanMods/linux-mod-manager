// gamechooser.js - the "/" route: a card per configured game, or (issue 333)
// the real first-run setup flow when there are none
// (docs/plans/2026-08-31-serve-spa-design.md §Information architecture,
// §Mission Control: "Empty states: no games -> setup guidance"). The
// single/default-game redirect is main.js's (maybeRedirectFromChooser) -
// this component only ever renders when there is a real choice to make, or
// none configured at all.
//
// The first-run flow is GameDetectSection + GameAddForm (gameadd.js) - the
// SAME two components the Setup page's Games section uses for a game added
// later, so first-run and "add another game" cannot drift into two forms
// that behave differently. Landing a game here (from either flow) routes
// straight to its Mission Control, per the task brief - there is nothing
// else to configure before the library has something to show.

import { html, useState } from "../render.js";
import { navigate } from "../router.js";
import { resolveGamePath } from "../navigation.js";
import { GameDetectSection, GameAddForm } from "./gameadd.js";
import { SetupSources } from "./setupsources.js";

export function GameChooser({ games }) {
  if (games === null) {
    return html`<p class="app-booting">Loading your games…</p>`;
  }

  if (games.length === 0) {
    return html`<${FirstRunSetup} />`;
  }

  return html`
    <div class="game-chooser" data-hydrated="true">
      <h1 class="section-header">Choose a game</h1>
      <div class="game-chooser__grid">
        ${games.map((game) => html`<${GameCard} key=${game.id} game=${game} />`)}
      </div>
    </div>
  `;
}

function GameCard({ game }) {
  const [pending, setPending] = useState(false);
  const profileCount = game.profiles?.length ?? 0;

  async function open() {
    setPending(true);
    const path = await resolveGamePath(game.id).catch(() => null);
    setPending(false);
    if (path) navigate(path);
  }

  return html`
    <button type="button" class="game-card" onClick=${open} disabled=${pending}>
      <span class="game-card__name">${game.name}</span>
      ${game.is_default && html`<span class="game-card__default">Default</span>`}
      <span class="game-card__meta">
        ${game.mod_count} mod${game.mod_count === 1 ? "" : "s"} ·
        ${profileCount} profile${profileCount === 1 ? "" : "s"}
      </span>
    </button>
  `;
}

/** FirstRunSetup is the "no games configured yet" state: the same detect
 * and manual-add flows the Setup page's Games section offers, since a
 * first-time install and "add another game" later are the same problem.
 * A game landing from either flow navigates straight to its Mission
 * Control - there is nothing else to configure before the library has
 * something to show. */
function FirstRunSetup() {
  // Bumped whenever the custom-source section below changes the registry,
  // and handed to the game form as its refreshKey: the form fetches the
  // source list once on mount, so a source defined right here would
  // otherwise be missing from the very picker this section exists to fill
  // (C-4).
  const [sourcesVersion, bumpSources] = useState(0);
  // detected (issue 206) is the row an uncurated "Add with details…" click
  // hands up here, prefilling the manual form below it.
  const [detected, setDetected] = useState(null);

  async function goTo(gameID) {
    const path = await resolveGamePath(gameID).catch(() => null);
    if (path) navigate(path);
  }

  function onDetected(result) {
    const first = result.saved?.[0];
    if (first) goTo(first);
  }

  function onAdded(entry) {
    goTo(entry.id);
  }

  return html`
    <div
      class="game-chooser game-chooser--first-run"
      data-hydrated="true"
      data-testid="first-run-setup"
    >
      <h1 class="section-header">Set up your first game</h1>
      <p class="empty-state__hint">
        No games are configured yet. Detect one automatically, or add it by
        hand.
      </p>
      <div class="setup-page__sections">
        <${GameDetectSection}
          onAdded=${onDetected}
          onAddWithDetails=${setDetected}
        />
        <${GameAddForm}
          onAdded=${onAdded}
          refreshKey=${sourcesVersion}
          detected=${detected}
        />
        ${
          /* C-4, epic live review: the dead end a custom-source user hit.
          The source editor lived only at /g/{game}/{profile}/setup, which
          needs a game; a game could only be added against a source that
          already existed. So a directory-source user - the case the README
          leads with - could not start at all without hand-editing
          games.yaml. Nothing about this editor is game-scoped (its routes
          are /api/v1/sources, flat), so it belongs here as much as it does
          on Setup, and the add form above will list whatever it creates. */ ""
        }
        <details
          class="setup-add__custom-sources"
          data-testid="first-run-sources"
        >
          <summary>Define a custom source first</summary>
          <p class="empty-state__hint">
            A game can only be added against a source that already exists. If
            your mods come from a local directory, a manifest or your own API,
            define that source here and it will appear in the form above.
          </p>
          <${SetupSources} onChanged=${() => bumpSources((v) => v + 1)} />
        </details>
      </div>
    </div>
  `;
}
