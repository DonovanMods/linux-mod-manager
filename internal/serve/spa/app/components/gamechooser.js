// gamechooser.js - the "/" route: a card per configured game, or the setup
// guidance empty state when there are none
// (docs/plans/2026-08-31-serve-spa-design.md §Information architecture,
// §Mission Control: "Empty states: no games -> setup guidance"). The
// single/default-game redirect is main.js's (maybeRedirectFromChooser) -
// this component only ever renders when there is a real choice to make, or
// none configured at all.

import { html, useState } from "../render.js";
import { navigate } from "../router.js";
import { resolveGamePath } from "../navigation.js";

export function GameChooser({ games }) {
  if (games === null) {
    return html`<p class="app-booting">Loading&#8230;</p>`;
  }

  if (games.length === 0) {
    return html`
      <div class="game-chooser" data-hydrated="true">
        <p class="section-header">Choose a game</p>
        <div class="empty-state">
          <p>No games are configured yet.</p>
          <p class="empty-state__hint">
            Run <code>lmm game add</code> from the command line to get started -
            a setup surface for the web UI lands in a later unit.
          </p>
        </div>
      </div>
    `;
  }

  return html`
    <div class="game-chooser" data-hydrated="true">
      <p class="section-header">Choose a game</p>
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
