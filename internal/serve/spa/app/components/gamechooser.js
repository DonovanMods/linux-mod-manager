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
        <${GameDetectSection} onAdded=${onDetected} />
        <${GameAddForm} onAdded=${onAdded} />
      </div>
    </div>
  `;
}
