// modpath.js - a game's mod path in the web UI (issue 460, the web half of
// issue 427): the warning every game document carries when its mod_path needs
// the user, the action that repairs it, and the refusal a move answers when
// files are still deployed under the old one.
//
// Core decides all of it. `mod_path_error` is ModPathProblem's sentence,
// present only when the directory needs attention (one lmm deployed into
// has gone, typically); `GameModPathInUseError` carries the ORDERED steps
// that clear a refused move. This file renders them and invents nothing.

import { html } from "../render.js";
import { codeSpans } from "../errortext.js";
import { navigate, modPathEditPath } from "../router.js";

/**
 * ModPathWarning is a game's mod_path_error as a warning, with a "Set mod
 * path…" action that opens Setup > Games on that game's mod-path editor.
 * Renders nothing for a game whose mod_path is fine.
 *
 * `onSetModPath` replaces the navigation where the editor is already on
 * screen (the Games table itself); `suggested` prefills the editor when a
 * caller holds core's suggestion (ModPathMissingError.suggested_mod_path -
 * only an error envelope carries it, never a row).
 */
export function ModPathWarning({
  error,
  gameID,
  route,
  suggested = "",
  onSetModPath,
}) {
  if (!error) return null;
  const open = () =>
    onSetModPath
      ? onSetModPath()
      : navigate(modPathEditPath(route.game, route.profile, gameID, suggested));
  return html`
    <div class="mod-path-warning" data-testid="mod-path-error" role="note">
      <p class="mod-path-warning__text">
        <span class="badge badge--warn">Mod path</span> ${codeSpans(error)}
      </p>
      <button
        type="button"
        class="button button--small"
        data-action="set-mod-path"
        data-game=${gameID}
        onClick=${open}
      >
        Set mod path…
      </button>
    </div>
  `;
}

/**
 * modPathInUseFor returns a core.GameModPathInUseError's details, or null -
 * identified by structure (the move's own two paths beside the per-profile
 * counts), never by the message.
 */
export function modPathInUseFor(details) {
  if (
    !details ||
    typeof details.new_mod_path !== "string" ||
    !Array.isArray(details.profiles) ||
    typeof details.deployed_files !== "number"
  ) {
    return null;
  }
  return details;
}

/**
 * modPathInUseSteps turns the refusal into the ordered steps that clear it,
 * in the order core's own sentence gives them:
 *
 *   1. when the active profile lists mods whose files only another profile
 *      records (listed_unrecorded): release them from any other game that
 *      records them too (release_first), then `lmm profile apply`
 *      (needs_apply), then `lmm deploy` (needs_deploy) - each only when set;
 *   2. purge every profile that has files there ("purge first", which is
 *      the FIRST step whenever step 1 is empty);
 *   3. save the new mod path again;
 *   4. deploy the active profile into it (and apply it, when apply_after_move
 *      says a deploy alone will not put back every mod it lists).
 *
 * Each step is {text, command}: the command is what the terminal runs, the
 * text what the web UI user does.
 */
export function modPathInUseSteps(d) {
  const game = d.game_id;
  const active = d.active_profile;
  const steps = [];
  if (d.listed_unrecorded > 0) {
    for (const other of d.release_first ?? []) {
      steps.push({
        text: `Purge profile ${other.profile} of game ${other.game_id}, which records some of the same files.`,
        command: `lmm purge --game ${other.game_id} --profile ${other.profile}`,
      });
    }
    if (d.needs_apply) {
      steps.push({
        text: `Apply profile ${active}, so it records the ${d.listed_unrecorded} file(s) it lists that only another profile records.`,
        command: `lmm profile apply ${active} --game ${game}`,
      });
    }
    if (d.needs_deploy) {
      steps.push({
        text: `Deploy profile ${active}, so it records the ${d.listed_unrecorded} file(s) it lists that only another profile records.`,
        command: `lmm deploy --game ${game}`,
      });
    }
  }
  const purged = d.profiles.map((p) => p.profile);
  if (d.listed_unrecorded > 0 && !purged.includes(active)) {
    purged.push(active);
    purged.sort();
  }
  for (const profile of purged) {
    const share = d.profiles.find((p) => p.profile === profile);
    steps.push({
      text: share
        ? `Purge profile ${profile} (${share.deployed_files} file(s) under ${d.mod_path}).`
        : `Purge profile ${profile}.`,
      command: `lmm purge --game ${game} --profile ${profile}`,
    });
  }
  steps.push({
    text: `Save the new mod path (${d.new_mod_path}) again.`,
    command: `lmm game edit ${game} --mod-path ${d.new_mod_path}`,
  });
  steps.push({
    text: `Deploy profile ${active} into the new mod path.`,
    command: `lmm deploy --game ${game}`,
  });
  if (d.apply_after_move) {
    steps.push({
      text: `Apply profile ${active}, which deploys the mods it lists whose files another game keeps in the old directory.`,
      command: `lmm profile apply ${active} --game ${game}`,
    });
  }
  return steps;
}

/**
 * ModPathInUse renders a refused move: the ordered steps first, core's full
 * sentence (which also names what no command can fix - listed_unavailable)
 * under them.
 */
export function ModPathInUse({ details, message }) {
  const steps = modPathInUseSteps(details);
  const unavailable = details.listed_unavailable ?? [];
  return html`
    <div class="mod-path-in-use" data-testid="mod-path-in-use">
      <p class="mod-path-in-use__title">
        ${
          // One string: htm drops the whitespace between a line break and
          // an interpolation, which fused "under" to the path.
          `${details.deployed_files} file(s) are deployed under ${details.mod_path}, so lmm will not move the mod path yet. In this order:`
        }
      </p>
      <ol class="mod-path-in-use__steps">
        ${steps.map(
          (s, i) => html`
            <li
              key=${i}
              class="mod-path-in-use__step"
              data-command=${s.command}
            >
              ${s.text} <code>${s.command}</code>
            </li>
          `,
        )}
      </ol>
      ${
        unavailable.length > 0 &&
        html`<p class="mod-path-in-use__note">
          ${unavailable.length} listed mod(s) cannot be deployed at the version
          the profile lists; the explanation below names the edit that fixes it.
        </p>`
      }
      <details class="mod-path-in-use__full">
        <summary>lmm's full explanation</summary>
        <p>${codeSpans(message)}</p>
      </details>
    </div>
  `;
}

/**
 * ModPathEditor is the mod-path input and its Save, with the three answers
 * a PUT can give rendered in place: a 400 marks the input, a 409 renders
 * ModPathInUse, anything else is shown verbatim.
 */
export function ModPathEditor({
  gameID,
  value,
  error,
  busy,
  onChange,
  onSave,
}) {
  const inputID = `mod-path-${gameID}`;
  const fieldError = error?.field === "mod_path" ? error.message : "";
  const inUse = modPathInUseFor(error?.details);
  return html`
    <div class="mod-path-editor" data-testid="mod-path-editor">
      <label class="plan__control" for=${inputID}>Mod path</label>
      <input
        id=${inputID}
        type="text"
        class="mono"
        name="mod-path"
        value=${value}
        disabled=${busy}
        aria-invalid=${fieldError ? "true" : undefined}
        aria-describedby=${fieldError ? `${inputID}-error` : undefined}
        onInput=${(e) => onChange(e.currentTarget.value)}
      />
      <button
        type="button"
        class="button button--small button--primary"
        data-action="save-mod-path"
        disabled=${busy}
        onClick=${onSave}
      >
        ${busy ? "Saving…" : "Save mod path"}
      </button>
      ${
        /* One live region, mounted with the editor and never removed, so the
        answer a Save gets is announced (the issue 442 rule): a region
        inserted together with its text is not reliably read out. */ ""
      }
      <div class="mod-path-editor__answer" role="status">
        ${
          fieldError &&
          html`<p
            class="modal__error"
            id=${`${inputID}-error`}
            data-testid="mod-path-field-error"
          >
            ${codeSpans(fieldError)}
          </p>`
        }
        ${inUse && html`<${ModPathInUse} details=${inUse} message=${error.message} />`}
        ${
          error &&
          !fieldError &&
          !inUse &&
          html`<p class="modal__error">${codeSpans(error.message)}</p>`
        }
      </div>
      <p class="empty-state__hint">
        The directory the game loads mods from. lmm refuses the move while files
        are deployed under the old one, and says what to run first.
      </p>
    </div>
  `;
}
