// gonepins.js - the "needs your attention" choices for a NexusMods
// collection's pinned files NexusMods no longer offers (issue 657).
// SourceCollectionSection (plan_profile_import.js) places GonePinsSection
// beside issue 654's attention group, so the import plan and the
// collection update plan both show it, and the summary's gone-pin count
// goes to it. ReofferedSection is the update view's
// other attention item: a mod the user skipped that the new version pins
// at an available file, which comes back unless they keep it skipped.
//
// Each gone pin offers the CLI's choices: install the current file of the
// same role instead (--use-current, suggested when on offer; a gone main
// file's stand-in is the mod's current main file, a gone optional's the
// current file of its category whose name matches), leave a gone optional
// out and install the rest (--skip-file), or skip the mod (--skip). A
// choice is a PLAN-time option - it changes what the plan installs - so
// picking one re-plans (actions.replanWith), and the preview always
// describes exactly what Confirm submits. Nothing is preselected: the
// import is the user's to decide, and Confirm stays disabled
// (confirmplan.js, gonePinsUndecided) until every pin is - a collection
// update's pin of a mod the user owns only when they take the collection's
// side of that conflict. Every string was sanitised by core.

import { html } from "../render.js";
import { displayVersion } from "../version.js";

/** pinKey is a gone pin's "<source-id>:<mod-id>", what the choices name. */
function pinKey(pin) {
  return `${pin.source_id}:${pin.mod_id}`;
}

/** describeFile names a current file as the CLI's plan line does. */
function describeFile(file) {
  if (!file) return "";
  const name = file.name || `file ${file.file_id}`;
  const version = displayVersion(file);
  return version ? `${name} (version ${version})` : name;
}

/** pinnedLine says what the collection pins for a gone pin. */
function pinnedLine(pin) {
  const files = (pin.file_ids ?? []).join(", ");
  const shown = displayVersion(pin);
  const version = shown ? `, version ${shown}` : "";
  return `The collection pins file ${files}${version}, which NexusMods no longer offers.`;
}

/** goneFileLine says what stands in for one gone file (core.
 * CollectionGoneFile) under "use the current file". */
function goneFileLine(pin, f) {
  const name = f.name || `File ${f.file_id}`;
  const role = f.role === "main" ? "its main file" : "an optional file";
  if (f.replacement)
    return `${name} (${role}): ${f.role === "main" ? "current main file" : "current file"} ${describeFile(f.replacement)}`;
  if (pin.current && (pin.not_replaced ?? []).includes(f.file_id))
    return `${name} (${role}): not replaced`;
  return f.role === "main"
    ? `${name} (${role}): NexusMods offers no other file for it`
    : `${name} (${role}): no current file of the same kind matches it`;
}

/** gonePinDecision words a decided pin's choice, as the CLI does; "" while
 * it is undecided. */
export function gonePinDecision(pin) {
  switch (pin?.choice) {
    case "use_current":
      return "The current file of the same role will be installed instead.";
    case "skip_file":
      return "The gone file is left out; the rest of the mod is installed.";
    case "skip":
      return "Skipped.";
    default:
      return "";
  }
}

/** modsPin is "1 mod pins" or "N mods pin". */
export function modsPin(n) {
  return n === 1 ? "1 mod pins" : `${n} mods pin`;
}

/** gonePinsUndecided reports whether a converted collection (core.
 * ConvertedCollection) has a gone pin nobody chose for that its Apply
 * needs decided: a conflict's pin only when overridden(key) - the user
 * takes the collection's side of it. Confirm waits on one. */
export function gonePinsUndecided(source, overridden) {
  return (source?.gone_pins ?? []).some(
    (pin) =>
      !pin.choice && (!pin.conflict || Boolean(overridden?.(pinKey(pin)))),
  );
}

/** GonePinsSection renders a converted collection's gone pins with their
 * choices; nothing when it has none. */
export function GonePinsSection({ source: c, actions, sectionRef }) {
  const pins = c?.gone_pins ?? [];
  if (pins.length === 0) return null;
  const undecided = pins.filter((pin) => !pin.choice);

  /** choose re-plans with key decided as choice, every other pin as it
   * is: an "all" choice is spelt out per pin, so changing one pin keeps
   * the others' answers. */
  function choose(pin, choice) {
    const sets = {
      use_current: new Set(),
      skip_file: new Set(),
      skip: new Set(),
    };
    for (const p of pins) if (p.choice) sets[p.choice]?.add(pinKey(p));
    const key = pinKey(pin);
    for (const set of Object.values(sets)) set.delete(key);
    sets[choice].add(key);
    actions.replanWith({
      use_current: [...sets.use_current].sort(),
      use_current_all: false,
      skip_file: [...sets.skip_file].sort(),
      skip: [...sets.skip].sort(),
    });
  }
  function useCurrentForAll() {
    const keep = (choice) =>
      pins
        .filter((p) => p.choice === choice)
        .map(pinKey)
        .sort();
    actions.replanWith({
      use_current: [],
      use_current_all: true,
      skip_file: keep("skip_file"),
      skip: keep("skip"),
    });
  }

  return html`
    <section
      class="plan__section plan__attention"
      data-testid="gone-pins"
      aria-labelledby="gone-pins-heading"
      ref=${sectionRef}
      tabindex="-1"
    >
      <h3 class="plan__heading" id="gone-pins-heading">
        ${`Needs your attention - ${modsPin(pins.length)} a file NexusMods no longer offers`}
      </h3>
      <p class="plan__note">
        Choose for each: the current file of the same role, leaving a gone
        optional file out, or leaving the mod out. Nothing is imported until
        each is decided.
      </p>
      <ul class="plan__mods">
        ${pins.map((pin) => {
          const key = pinKey(pin);
          const name = pin.name || key;
          return html`<li
            key=${key}
            class=${`plan__mod${pin.choice ? "" : " plan__mod--warn"}`}
            data-gone-pin=${key}
            data-choice=${pin.choice || ""}
          >
            <fieldset class="form-section">
              <legend class="plan__mod-name">${name}</legend>
              <p class="plan__note">${pinnedLine(pin)}</p>
              <ul class="plan__paths">
                ${(pin.files ?? []).map(
                  (f) =>
                    html`<li key=${f.file_id} data-role=${f.role}>
                      ${goneFileLine(pin, f)}
                    </li>`,
                )}
              </ul>
              ${
                pin.conflict &&
                !pin.choice &&
                html`<p class="plan__note">
                  You changed this mod: your version is kept unless you take the
                  collection's, and only then does this need deciding.
                </p>`
              }
              <label class="form-check">
                <input
                  type="radio"
                  name=${`gone-pin-${key}`}
                  data-testid="gone-pin-use-current"
                  checked=${pin.choice === "use_current"}
                  disabled=${!pin.current}
                  onChange=${() => choose(pin, "use_current")}
                />
                ${
                  pin.current
                    ? "Install the current file instead (suggested)"
                    : "Install the current file instead: no current file stands in for it"
                }
              </label>
              ${
                pin.can_skip_file &&
                html`<label class="form-check">
                  <input
                    type="radio"
                    name=${`gone-pin-${key}`}
                    data-testid="gone-pin-skip-file"
                    checked=${pin.choice === "skip_file"}
                    onChange=${() => choose(pin, "skip_file")}
                  />
                  Skip this file (install the rest)
                </label>`
              }
              <label class="form-check">
                <input
                  type="radio"
                  name=${`gone-pin-${key}`}
                  data-testid="gone-pin-skip"
                  checked=${pin.choice === "skip"}
                  onChange=${() => choose(pin, "skip")}
                />
                ${pin.can_skip_file ? "Skip the whole mod" : "Skip this mod"}
              </label>
            </fieldset>
          </li>`;
        })}
      </ul>
      ${
        undecided.filter((p) => p.current).length > 1 &&
        html`<div class="form-actions">
          <button
            type="button"
            class="button"
            data-testid="gone-pins-use-current-all"
            onClick=${useCurrentForAll}
          >
            Install the current file for every undecided mod
          </button>
        </div>`
      }
    </section>
  `;
}

/** ReofferedSection renders a collection update's skipped mods that the
 * new version pins anew at an available file (core.CollectionUpdatePlan.
 * reoffered): each comes back by default, and its "Keep it skipped" box
 * re-plans with keep_skipped (`--keep-skipped`), as an optional item's
 * toggle does. Its note names the file and version the collection now
 * pins. Nothing when there are none. */
export function ReofferedSection({ plan, actions }) {
  const items = plan?.reoffered ?? [];
  if (items.length === 0) return null;
  const kept = new Set(plan.keep_skipped ?? []);
  function toggle(key, on) {
    const next = new Set(kept);
    if (on) next.add(key);
    else next.delete(key);
    actions.replanWith({ keep_skipped: [...next].sort() });
  }
  return html`
    <section
      class="plan__section plan__attention"
      data-testid="update-reoffered"
      aria-labelledby="update-reoffered-heading"
    >
      <h3 class="plan__heading" id="update-reoffered-heading">
        ${`Needs your attention - ${items.length === 1 ? "1 skipped mod is" : `${items.length} skipped mods are`} pinned at an available file again`}
      </h3>
      <ul class="plan__mods">
        ${items.map((it) => {
          const key = `${it.source_id}:${it.mod_id}`;
          return html`<li
            key=${key}
            class="plan__mod"
            data-reoffered=${key}
            data-keep=${it.keep_skipped ? "true" : "false"}
          >
            <span class="plan__mod-name">${it.name || key}</span>${" "}
            <span class="plan__mod-detail"
              >${it.keep_skipped ? "kept skipped" : "will be added"}</span
            >
            ${it.note && html`<p class="plan__note">${it.note}</p>`}
            <label class="form-check">
              <input
                type="checkbox"
                data-testid="update-keep-skipped"
                checked=${!!it.keep_skipped}
                onChange=${(e) => toggle(key, e.currentTarget.checked)}
              />
              Keep it skipped
            </label>
          </li>`;
        })}
      </ul>
    </section>
  `;
}
