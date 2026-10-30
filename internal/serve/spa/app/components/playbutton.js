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
// Only the game's active profile can be launched - its mods are the ones
// deployed - and picking a profile in the top bar only VIEWS it. So on any
// other profile Play is aria-disabled too, its reason pointing at the
// profile menu's "Switch and deploy…".
//
// A launch is not a job - the server answers once the game's systemd unit
// has started - so it has no InlineJob morph. While its POST is out the
// label names it ("Starting Valheim…"), painted only past the shared show
// delay (loading.js), since most launches answer well before that. Deploy &
// launch holds the button the whole way: aria-disabled while its deploy job
// runs (the Deploy control shows that progress), then the launch's label.

import { html } from "../render.js";
import { useLastWhile, useShownWhile } from "../loading.js";

/** PLAY_NOT_ACTIVE_REASON is why Play is off on a profile that is not the
 * game's active one - and what a launch refused for that
 * (core.ErrProfileNotActive's 409) says. */
export const PLAY_NOT_ACTIVE_REASON = `Switch to this profile to play it: "Switch and deploy…" in the profile menu.`;

/** PLAY_DEPLOYING_REASON is Play's reason while Deploy & launch's deploy
 * job runs. */
const PLAY_DEPLOYING_REASON = "Launching once the deploy finishes.";

/** playUnavailableReason is why status's game cannot be launched for
 * profile, or "" when it can. A status with no launch member at all comes
 * from a server that cannot launch anything; a status that names no active
 * profile leaves the question to the server. */
export function playUnavailableReason(status, profile) {
  if (!status) return "Loading…";
  const launch = status.launch;
  if (!launch?.method) {
    return launch?.reason
      ? `Can't launch ${status.name || status.id}: ${launch.reason}`
      : "This lmm can't launch games.";
  }
  if (profile && status.active_profile && profile !== status.active_profile) {
    return PLAY_NOT_ACTIVE_REASON;
  }
  return "";
}

/**
 * PlayButton launches the selected game's active profile.
 *
 * `action` is the button's data-action - "play" for the top bar's, another
 * for any other placement, so each is addressable on its own - and `small`
 * draws it at the card-row size.
 */
export function PlayButton({ state, status, actions, action = "play", small }) {
  const launching = state.launching;
  const inFlight = Boolean(launching);
  const deploying = launching?.phase === "deploy";
  const reason =
    playUnavailableReason(status, state.route?.profile) ||
    (deploying ? PLAY_DEPLOYING_REASON : "");
  const shown = useShownWhile(inFlight && !deploying);
  const name = useLastWhile(inFlight, launching?.name);
  const describedBy = reason ? `${action}-reason` : undefined;

  return html`
    <button
      type="button"
      class="button${small ? " button--small" : ""}"
      data-action=${action}
      aria-disabled=${reason || inFlight ? "true" : undefined}
      aria-busy=${inFlight ? "true" : undefined}
      aria-describedby=${describedBy}
      data-tooltip=${reason || undefined}
      onClick=${() => {
        if (!reason && !inFlight) actions.launchGame();
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
