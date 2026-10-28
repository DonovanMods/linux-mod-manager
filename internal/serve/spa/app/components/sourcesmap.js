// sourcesmap.js - the game↔source mapping editor (C-4, epic live review).
//
// The dead end it exists to close: the custom-source editor lives on Setup,
// which needs a game; a game could only be added against a source that
// already existed; and NEITHER frontend could add a source to an existing
// game. So a directory-source user - the case the README leads with -
// created their source, watched it sit at "In use: —" forever, and had to
// stop the server and hand-edit games.yaml.
//
// One component, two callers, because it is one question asked at two
// moments: the add form asks it about a game that does not exist yet
// (setupgames.js/gameadd.js), the Games table asks it about one that does.
// Both submit the SAME shape - the whole map - because
// PUT /api/v1/games/{id} is a replacement, not a patch: an omitted id is
// removed, which is the only way "stop using this source" is expressible at
// all.

import { html } from "../render.js";

/**
 * SourcesMapEditor renders one row per registered source: a checkbox for
 * whether the game maps it, and the identifier it maps to.
 *
 * An EMPTY identifier is legal and common - it is how a directory source is
 * normally configured, and the built-in sources want the game's own id
 * there instead - so the checkbox, not the text, is what says "mapped".
 *
 * `value` is the map as it stands ({sourceID: identifier}); onChange is
 * called with the whole next map, never a delta.
 */
export function SourcesMapEditor({ sources, value, onChange, disabled }) {
  const map = value ?? {};

  function setMapped(id, mapped) {
    const next = { ...map };
    if (mapped) next[id] = next[id] ?? "";
    else delete next[id];
    onChange(next);
  }

  function setIdentifier(id, identifier) {
    onChange({ ...map, [id]: identifier });
  }

  return html`
    <div class="sources-map" data-testid="sources-map">
      ${(sources ?? []).map(
        (s) => html`
          <div class="sources-map__row" key=${s.id}>
            <label class="sources-map__toggle">
              <input
                type="checkbox"
                name=${`source-${s.id}`}
                checked=${Object.hasOwn(map, s.id)}
                disabled=${disabled}
                onChange=${(e) => setMapped(s.id, e.currentTarget.checked)}
              />
              ${s.name ?? s.id}${" "}
              <span class="mono empty-state__hint">${s.id}</span>
            </label>
            <input
              type="text"
              class="sources-map__identifier"
              name=${`identifier-${s.id}`}
              aria-label=${`Identifier for ${s.id}`}
              placeholder="identifier (may be empty)"
              value=${map[s.id] ?? ""}
              disabled=${disabled || !Object.hasOwn(map, s.id)}
              onInput=${(e) => setIdentifier(s.id, e.currentTarget.value)}
            />
          </div>
        `,
      )}
    </div>
  `;
}
