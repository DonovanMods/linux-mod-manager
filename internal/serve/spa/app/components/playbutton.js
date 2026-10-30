// playbutton.js - the Play button (issue 501): the top bar's, next to
// Deploy, and the Health card's on a BepInEx "launch the game once to
// confirm" finding, whose remedy launching is.
//
// It reads core.GameStatus.launch: a method means the game can be launched,
// and a reason (with no method) says why it cannot - games.yaml needs a
// launch.command, say. Then the button is aria-disabled rather than
// disabled, so it stays focusable and its reason reaches everyone: as the
// tooltip on hover and focus, and as its accessible description.
//
// A launch is not a job - the server answers once the game's systemd unit
// has started - so it has no InlineJob morph. While its POST is out the
// label names it ("Starting Valheim…"), painted only past the shared show
// delay (loading.js), since most launches answer well before that.

import { html } from "../render.js";
import { useLastWhile, useShownWhile } from "../loading.js";

/** playUnavailableReason is why status's game cannot be launched, or ""
 * when it can. A status with no launch member at all comes from a server
 * that cannot launch anything. */
export function playUnavailableReason(status) {
  if (!status) return "Loading…";
  const launch = status.launch;
  if (launch?.method) return "";
  return launch?.reason
    ? `Can't launch ${status.name || status.id}: ${launch.reason}`
    : "This lmm can't launch games.";
}

/**
 * PlayButton launches the selected game's active profile.
 *
 * `action` is the button's data-action - "play" for the top bar's, another
 * for any other placement, so each is addressable on its own - and `small`
 * draws it at the card-row size.
 */
export function PlayButton({ state, status, actions, action = "play", small }) {
  const reason = playUnavailableReason(status);
  const launching = state.launching;
  const inFlight = Boolean(launching);
  const shown = useShownWhile(inFlight);
  const name = useLastWhile(inFlight, launching?.name);
  const describedBy = reason ? `${action}-reason` : undefined;

  return html`
    <button
      type="button"
      class="button${small ? " button--small" : ""}"
      data-action=${action}
      aria-disabled=${reason ? "true" : undefined}
      aria-busy=${inFlight ? "true" : undefined}
      aria-describedby=${describedBy}
      data-tooltip=${reason || undefined}
      onClick=${() => {
        if (!reason) actions.launchGame();
      }}
    >
      ${shown ? `Starting ${name}…` : "Play"}
    </button>
    ${
      reason &&
      html`<span id=${describedBy} class="visually-hidden">${reason}</span>`
    }
  `;
}
