// gameadd.js - the two ways a game comes to exist (issue 333, design doc
// §Scope: "first-run flow in the game chooser, game add/detect UI"):
// GameDetectSection ("Detect games") and GameAddForm ("Add a game
// manually"). Both are used TWICE - by the first-run flow at "/"
// (gamechooser.js, when GET /api/v1/games answers empty) and by the Setup
// page's Games section (setupgames.js, for a game added later) - which is
// exactly why they live in their own file rather than either caller's.
//
// Neither goes through the confirm-plan framework: POST /api/v1/games and
// POST /api/v1/games/detect are sanctioned single-step writes with nothing
// to preview beforehand (the same class enable/disable and the profile CRUD
// routes are) - there is no Plan for "add a game", only a form and a write.
//
// issue 206 widened both: GameDetectSection now scans with GET
// /api/v1/games/detect?all=1, so a Steam game lmm has no curated entry for
// is SHOWN rather than hidden - with an "Add with details…" action in
// place of the checkbox (it has no index to select with,
// GameDetectEntry.Index's own rule). GameAddForm gained a `detected` prop
// and its own "Pick an installed game…" control - either path prefills the
// form from a GameDetectEntry FOR DISPLAY (name, install path, a guessed
// mod path, the derived game id), then submits with `from_steam_app_id`
// plus only the fields the user actually edited: the form derives no slug,
// mod path or source map of its own (unit9a-task-report.md's wire note).
import { html, useEffect, useState } from "../render.js";
import {
  ApiError,
  addGame,
  applyGameDetect,
  detectGames,
  gameCatalog,
  listSources,
  updateGameSources,
} from "../api.js";
import { navigate, setupPath } from "../router.js";
import { SourcesMapEditor } from "./sourcesmap.js";

/**
 * GameDetectSection scans for Steam installs and offers to add the ones not
 * already configured. It scans WIDE (issue 206: `?all=1`) so an installed game
 * with no curated known-games entry is shown, not silently dropped - it
 * just has no checkbox, since a detect selection can only name a known row
 * (GameDetectEntry.Index is 0 for one). Already-configured rows are shown
 * but their control is disabled - `lmm game detect --select` can still
 * repair one from the CLI, but a checklist offering to silently overwrite
 * an existing game's default profile is not this surface's first-run job.
 */
export function GameDetectSection({ onAdded, onAddWithDetails }) {
  const [listing, setListing] = useState(null); // {games, warnings} | "error"
  const [error, setError] = useState(null);
  const [selected, setSelected] = useState(() => new Set());
  const [busy, setBusy] = useState(false);
  const [applyError, setApplyError] = useState(null);

  async function scan() {
    setListing(null);
    setError(null);
    setSelected(new Set());
    try {
      setListing(await detectGames(true));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  useEffect(() => {
    scan();
  }, []);

  function toggle(index) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });
  }

  async function addSelected() {
    setBusy(true);
    setApplyError(null);
    try {
      const result = await applyGameDetect([...selected].map(String));
      setSelected(new Set());
      await scan();
      onAdded?.(result);
    } catch (err) {
      setApplyError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const games = listing?.games ?? [];
  const hasUnknown = games.some((g) => !g.known);

  return html`
    <div class="setup-detect" data-testid="setup-detect">
      <h3 class="plan__heading">Detect games</h3>
      ${
        error &&
        html`
          <div class="empty-state empty-state--error">
            <p>Couldn't scan for Steam installs: ${error}</p>
            <button type="button" class="button button--small" onClick=${scan}>
              Retry
            </button>
          </div>
        `
      }
      ${
        listing === null && !error && html`<p class="app-booting">Scanning…</p>`
      }
      ${
        listing &&
        games.length === 0 &&
        html`<p class="empty-state__hint">
          No moddable Steam games were found on this machine.
        </p>`
      }
      ${
        listing &&
        (listing.warnings ?? []).length > 0 &&
        html`<p class="plan__note plan__note--warn">
          ${listing.warnings.join(" · ")}
        </p>`
      }
      ${
        hasUnknown &&
        html`<p class="empty-state__hint">
          A game with no checkbox isn't in lmm's curated list yet - use "Add
          with details…" to configure its source and mod path.
        </p>`
      }
      ${
        listing &&
        games.length > 0 &&
        html`
          <ul class="setup-detect__list">
            ${games.map(
              (g) => html`
                <li key=${g.steam_app_id} class="setup-detect__row">
                  ${
                    g.known
                      ? html`
                          <label class="setup-detect__name">
                            <input
                              type="checkbox"
                              checked=${selected.has(g.index)}
                              disabled=${g.already_configured}
                              onChange=${() => toggle(g.index)}
                            />
                            ${g.name}
                            <span class="badge badge--policy">Known</span>
                            ${
                              g.already_configured &&
                              html`<span class="badge"
                                >already configured</span
                              >`
                            }
                          </label>
                        `
                      : html`
                          <span class="setup-detect__name">
                            ${g.name}
                            ${
                              g.already_configured &&
                              html`<span class="badge"
                                >already configured</span
                              >`
                            }
                          </span>
                        `
                  }
                  <span class="mono setup-detect__path" title=${g.install_path}
                    >${g.install_path}</span
                  >
                  ${
                    !g.known &&
                    html`
                      <button
                        type="button"
                        class="button button--small"
                        data-action="add-with-details"
                        disabled=${g.already_configured}
                        onClick=${() => onAddWithDetails?.(g)}
                      >
                        Add with details…
                      </button>
                    `
                  }
                </li>
              `,
            )}
          </ul>
          <button
            type="button"
            class="button button--primary button--small"
            data-action="add-detected"
            disabled=${busy || selected.size === 0}
            onClick=${addSelected}
          >
            ${busy ? "Adding…" : `Add selected (${selected.size})`}
          </button>
          ${applyError && html`<p class="modal__error">${applyError}</p>`}
        `
      }
    </div>
  `;
}

// emptySpec is GameAddForm's own local state shape - the gameAddRequest
// members plus the UI-only "mode" (catalog search vs. a manual identifier)
// and the picked catalog match, if any.
/** authRequiredMessage is the catalog-401 message (Important 1): a live
 * link to the Authentication section when this form has a game/profile to
 * build one from (rendered inside the Setup page), plain text naming the
 * section when it does not (first-run, before any game exists). */
function authRequiredMessage(sourceName, game, profile) {
  if (!game || !profile) {
    return `Authenticate ${sourceName} first - see the Authentication section.`;
  }
  const href = setupPath(game, profile, "auth");
  return html`Authenticate ${sourceName} first - see the${" "}
    <a
      href=${href}
      onClick=${(e) => {
        e.preventDefault();
        navigate(href);
      }}
      >Authentication section</a
    >.`;
}

// KNOWN_FIELD_ERRORS is every GameSpecError.Field errorFor() below actually
// renders an input for. "game_id" (core.GameSpec.ID, the local games.yaml
// key) used to have no input at all - set only from a catalog match's own
// game_id - so a rejection naming it fell back to the form-wide banner
// (Minor 2). N-6, epic re-review, gave it a manual Advanced input, so it
// joins this set the same as every other named field. "from_steam_app_id"
// and "sources" (issue 206) deliberately stay OUT of this set - neither has a
// dedicated input (the app id names no field at all; a curated candidate's
// own source map isn't user-editable here) - so both fall to the form
// banner the same way an unmapped field always has; from_steam_app_id gets
// its own message and a Rescan control in submit()'s catch, below.
const KNOWN_FIELD_ERRORS = new Set([
  "source_id",
  "identifier",
  "name",
  "install_path",
  "mod_path",
  "game_id",
]);

// identifierHints names the per-source example/placeholder the "Identifier
// with that source" field otherwise gave no clue about at all (first-run
// readiness item 4): a first-run user who cannot use detect had to guess
// what an "identifier" even looks like. Keyed by the two built-in source
// ids - source.TypeLabelOf reports both as the same "built-in" type
// (app.SourceInfo carries no per-built-in distinction), so the id itself
// is the only signal the SPA has to tell them apart.
const identifierHints = {
  nexusmods: {
    placeholder: "skyrimspecialedition",
    hint: "The URL slug from this game's NexusMods page.",
  },
  curseforge: {
    placeholder: "432",
    hint: 'The numeric game id - use "Search this source\'s catalog" above to find it.',
  },
};

function emptySpec() {
  return {
    sourceID: "",
    // extraSources is the C-4 multi-select: sources BESIDES the primary one
    // this game should also map. POST /api/v1/games takes exactly one
    // source+identifier pair (it is what makes the game addressable at
    // all), so extras are applied by a PUT immediately afterwards - the
    // same replacement-shaped call the Games table's own editor makes.
    extraSources: {},
    query: "",
    matches: null, // null = not searched yet; [] = searched, nothing found
    noCatalog: false,
    identifier: "",
    name: "",
    installPath: "",
    modPath: "",
    gameID: undefined,
  };
}

// exactCatalogMatch mirrors core.ExactGameCatalogMatch client-side: the ONE
// case a catalog search auto-selects a result (issue 206's suggestion path) -
// exactly one match whose name equals `name`, compared case-insensitively
// after trimming. null for zero matches, no exact match, or two entries
// sharing the name - never "the only match" (a one-match search for
// "Hades" can still return "Hades II").
function exactCatalogMatch(matches, name) {
  const target = (name ?? "").trim().toLowerCase();
  if (!target || !matches) return null;
  const found = matches.filter(
    (m) => (m.name ?? "").trim().toLowerCase() === target,
  );
  return found.length === 1 ? found[0] : null;
}

/**
 * GameAddForm collects a GameSpec by hand: a source, then either a catalog
 * pick or a manual identifier (core.ErrNoGameCatalog's own 400 has no
 * `details.field` - unlike a bad query - which is the structural signal
 * used to fall back to the identifier field, never the message text), then
 * the display name and paths. A field-named 400 (core.GameSpecError) marks
 * the matching input rather than a generic banner.
 *
 * `detected` (issue 206) is an optional GameDetectEntry - one of GET
 * /api/v1/games/detect?all=1's rows - either handed in by a caller (an
 * uncurated row's "Add with details…") or picked from this form's own
 * "Pick an installed game…" control. It prefills the form FOR DISPLAY
 * only: install path becomes read-only (the browser never supplies one for
 * a detected game - unit9a-task-report.md's own wire rule), the mod path
 * field's placeholder shows the `<install>/mods` guess, and the Advanced
 * game-id field's placeholder shows the detected slug. Submitting sends
 * `from_steam_app_id` plus the source pair, and mod_path/game_id/name only
 * when they differ from what was prefilled - core derives the rest
 * (GameSpecFromDetected), exactly as `lmm game add --from-detected` does.
 * Choosing a source while a detected row is active auto-runs the catalog
 * search by the row's own name (mirroring the CLI's autoPickName) and
 * pre-selects a single exact-name match without submitting anything.
 * Clearing calls `onClearDetected` so the caller drops its own reference to
 * the row too - otherwise re-clicking the same "Add with details…" hands
 * back an identical object and the effect above never re-fires (#206).
 *
 * game/profile are optional: they are set when this form renders inside an
 * already-established Setup page (setupgames.js), and let a 401 from the
 * catalog search (Important 1) link straight to the Authentication
 * section. First-run (gamechooser.js) has no game yet - there is nothing
 * to build that link from - so the same 401 there falls back to naming the
 * section in plain text.
 */
export function GameAddForm({
  onAdded,
  game,
  profile,
  refreshKey,
  detected,
  onClearDetected,
}) {
  const [sources, setSources] = useState(null);
  const [spec, setSpec] = useState(emptySpec);
  const [detectedRow, setDetectedRow] = useState(null);
  const [staleDetected, setStaleDetected] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerListing, setPickerListing] = useState(null);
  const [pickerError, setPickerError] = useState(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState(null);
  const [busy, setBusy] = useState(false);
  const [fieldError, setFieldError] = useState(null); // {field, reason}
  const [formError, setFormError] = useState(null);

  // refreshKey (C-4) is the caller's way of saying "the source registry
  // changed": first run renders the custom-source editor beside this form,
  // and a source defined there has to appear in this picker without a
  // reload. Every other caller omits it and this runs exactly once.
  useEffect(() => {
    listSources()
      .then((rows) => setSources(rows.filter((r) => r.type !== "error")))
      .catch(() => setSources([]));
  }, [refreshKey]);

  // A `detected` prop applies once per row it is handed - a fresh object
  // from a fresh "Add with details…" click, since this form's own picker
  // (below) applies a row directly without going through the prop at all.
  // The effect keys on `detected` by object IDENTITY, so clearDetected()
  // below must also clear the parent's copy (onClearDetected) - otherwise
  // re-clicking the same row hands back the same reference, the effect
  // does not re-run, and the click silently does nothing (#206 fix wave).
  useEffect(() => {
    if (detected) applyDetected(detected);
    // eslint-disable-next-line
  }, [detected]);

  function patch(fields) {
    setSpec((prev) => ({ ...prev, ...fields }));
  }

  function applyDetected(row) {
    setDetectedRow(row);
    setSpec({ ...emptySpec(), name: row.name, query: row.name });
    setPickerOpen(false);
    setStaleDetected(false);
    setFormError(null);
    setFieldError(null);
  }

  function clearDetected() {
    setDetectedRow(null);
    setSpec(emptySpec());
    setStaleDetected(false);
    setFormError(null);
    onClearDetected?.();
  }

  async function openPicker() {
    setPickerOpen(true);
    setPickerError(null);
    setPickerListing(null);
    try {
      setPickerListing(await detectGames(true));
    } catch (err) {
      setPickerError(err instanceof ApiError ? err.message : String(err));
    }
  }

  // rescan answers a stale `from_steam_app_id` (the candidate this form
  // held no longer matches a live scan - detectedGameCandidate's own web
  // twin): drop the stale row and re-open the picker against a fresh scan,
  // rather than leaving the user stuck re-submitting the same dead app id.
  async function rescan() {
    clearDetected();
    await openPicker();
  }

  function pickMatch(m) {
    patch({
      identifier: m.identifier,
      name: spec.name || m.name,
      // A detected candidate's own derived slug already outranks a catalog
      // match's game id (GameSpecFromDetected fills spec.ID before any
      // catalog step runs, in both the CLI and here) - so in detected mode
      // this leaves gameID untouched rather than overwriting it, matching
      // applyGameCatalogMatch's real precedence (cmd/lmm/game_add.go).
      gameID: detectedRow ? spec.gameID : m.game_id,
    });
  }

  async function runSearch(sourceID, query, { autoPickName } = {}) {
    if (!sourceID || !query.trim()) return;
    const sourceName =
      (sources ?? []).find((s) => s.id === sourceID)?.name ?? sourceID;
    setSearching(true);
    setSearchError(null);
    try {
      const report = await gameCatalog(sourceID, query.trim());
      patch({ matches: report.matches, noCatalog: false });
      if (autoPickName) {
        const m = exactCatalogMatch(report.matches, autoPickName);
        if (m) pickMatch(m);
      }
    } catch (err) {
      // Status is read BEFORE `details.field` (Important 1): a 401 - the
      // source refused for want of a credential (domain.ErrAuthRequired) -
      // has no Details() of its own, so it would otherwise fall into the
      // "no searchable catalog" branch below and tell the user the wrong
      // thing entirely.
      if (err instanceof ApiError && err.status === 401) {
        patch({ matches: null, noCatalog: false });
        setSearchError(authRequiredMessage(sourceName, game, profile));
        return;
      }
      if (err instanceof ApiError && err.details?.field) {
        setSearchError(err.details.reason || err.message);
      } else {
        // No `details.field` on a 400 from this endpoint means the source
        // itself has no searchable catalog (core.ErrNoGameCatalog) -
        // structural, not a message match (game_add.go's own rule).
        patch({ matches: null, noCatalog: true });
      }
    } finally {
      setSearching(false);
    }
  }

  async function search(e) {
    e.preventDefault();
    await runSearch(spec.sourceID, spec.query);
  }

  // A detected row's source choice auto-runs the catalog search by the
  // row's own name (issue 206, mirroring the CLI's autoPickName) - the user
  // still has to pick the source, but not re-type what lmm already scanned.
  useEffect(() => {
    if (!detectedRow || !spec.sourceID) return;
    runSearch(spec.sourceID, detectedRow.name, {
      autoPickName: detectedRow.name,
    });
    // eslint-disable-next-line
  }, [detectedRow, spec.sourceID]);

  async function submit(e) {
    e.preventDefault();
    setBusy(true);
    setFieldError(null);
    setFormError(null);
    setStaleDetected(false);
    try {
      let entry;
      if (detectedRow) {
        // issue 206: the app id is the whole prefill - core re-scans and fills
        // name/install path/mod path/game id/source map itself
        // (GameSpecFromDetected). Only fields the user actually changed
        // from what was prefilled ride along as overrides.
        const body = {
          from_steam_app_id: detectedRow.steam_app_id,
          source_id: spec.sourceID,
          identifier: spec.identifier,
        };
        if (spec.name && spec.name !== detectedRow.name) body.name = spec.name;
        if (spec.gameID) body.game_id = spec.gameID;
        if (spec.modPath) body.mod_path = spec.modPath;
        entry = await addGame(body);
      } else {
        entry = await addGame({
          source_id: spec.sourceID,
          identifier: spec.identifier,
          name: spec.name,
          game_id: spec.gameID || undefined,
          install_path: spec.installPath,
          mod_path: spec.modPath || undefined,
        });
      }
      // The extras ride a second call, and the game is REAL by the time it
      // runs - so a failure here is reported without pretending the add
      // itself failed, and the row the caller is handed is the one that
      // actually exists.
      const extras = Object.keys(spec.extraSources).filter(
        (id) => id !== spec.sourceID,
      );
      if (extras.length > 0) {
        const map = { [spec.sourceID]: spec.identifier };
        for (const id of extras) map[id] = spec.extraSources[id];
        try {
          entry = await updateGameSources(entry.id, map);
        } catch (err) {
          setFormError(
            `${spec.name} was added, but its extra sources were not mapped: ` +
              (err instanceof ApiError ? err.message : String(err)),
          );
        }
      }
      setSpec(emptySpec());
      setDetectedRow(null);
      onAdded?.(entry);
    } catch (err) {
      // A stale from_steam_app_id (Task A's own doc: "no installed Steam
      // game has that app id") gets its own message and a Rescan control -
      // there is no input to mark, and the fix is a fresh scan, not a
      // retyped value.
      if (
        err instanceof ApiError &&
        err.details?.field === "from_steam_app_id"
      ) {
        setStaleDetected(true);
        setFormError(err.details.reason || err.message);
        return;
      }
      // A field this form has no input for (e.g. "game_id", when a
      // catalog-derived id fails GameSpecError's path-safety check, or
      // "sources", when a curated candidate's own map names an
      // unregistered source) must still tell the user SOMETHING rather
      // than silently un-busying the button (Minor 2): fall back to the
      // form-wide banner whenever no input matches the named field.
      if (
        err instanceof ApiError &&
        err.details?.field &&
        KNOWN_FIELD_ERRORS.has(err.details.field)
      ) {
        setFieldError(err.details);
      } else {
        setFormError(err instanceof ApiError ? err.message : String(err));
      }
    } finally {
      setBusy(false);
    }
  }

  const errorFor = (field) =>
    fieldError?.field === field ? fieldError.reason : null;
  const installPathReady = detectedRow || spec.installPath;
  const modPathPlaceholder = detectedRow
    ? `${detectedRow.install_path}/mods`
    : spec.installPath
      ? `${spec.installPath}/mods`
      : "";

  return html`
    <form class="setup-add" data-testid="setup-add-game" onSubmit=${submit}>
      <h3 class="plan__heading">Add a game manually</h3>

      <button
        type="button"
        class="button button--small"
        data-action="pick-installed"
        onClick=${() => (pickerOpen ? setPickerOpen(false) : openPicker())}
      >
        ${pickerOpen ? "Hide installed games" : "Pick an installed game…"}
      </button>

      ${
        pickerOpen &&
        html`
          <div class="setup-add__picker" data-testid="setup-add-picker">
            ${pickerError && html`<p class="modal__error">${pickerError}</p>`}
            ${
              pickerListing === null &&
              !pickerError &&
              html`<p class="app-booting">Scanning…</p>`
            }
            ${
              pickerListing &&
              (pickerListing.games ?? []).length === 0 &&
              html`<p class="empty-state__hint">
                No installed Steam games were found.
              </p>`
            }
            ${
              pickerListing &&
              (pickerListing.games ?? []).length > 0 &&
              html`
                <ul class="setup-detect__list">
                  ${pickerListing.games.map(
                    (g) => html`
                      <li key=${g.steam_app_id} class="setup-detect__row">
                        <button
                          type="button"
                          class="button button--small"
                          data-action="pick-installed-row"
                          disabled=${g.already_configured}
                          onClick=${() => applyDetected(g)}
                        >
                          ${g.name}
                        </button>
                        ${g.known && html`<span class="badge badge--policy">Known</span>`}
                        ${
                          g.already_configured &&
                          html`<span class="badge">already configured</span>`
                        }
                        <span
                          class="mono setup-detect__path"
                          title=${g.install_path}
                          >${g.install_path}</span
                        >
                      </li>
                    `,
                  )}
                </ul>
              `
            }
          </div>
        `
      }
      ${
        detectedRow &&
        html`
          <div class="plan__note" data-testid="setup-add-detected">
            Prefilled from <strong>${detectedRow.name}</strong> (Steam app
            ${detectedRow.steam_app_id}).${" "}
            <button
              type="button"
              class="button button--small"
              data-action="clear-detected"
              onClick=${clearDetected}
            >
              Clear
            </button>
          </div>
        `
      }

      <label class="plan__control">
        Source
        <select
          name="add-source"
          value=${spec.sourceID}
          onChange=${(e) =>
            patch({
              sourceID: e.currentTarget.value,
              matches: null,
              noCatalog: false,
              identifier: "",
            })}
        >
          <option value="">Choose a source…</option>
          ${(sources ?? []).map(
            (s) => html`<option key=${s.id} value=${s.id}>${s.name}</option>`,
          )}
        </select>
      </label>
      ${errorFor("source_id") && html`<p class="modal__error">${errorFor("source_id")}</p>`}
      ${
        spec.sourceID &&
        html`
          <div class="setup-add__catalog">
            <label class="plan__control">
              Search this source's catalog
              <input
                type="text"
                name="add-query"
                value=${spec.query}
                onInput=${(e) => patch({ query: e.currentTarget.value })}
              />
            </label>
            <button
              type="button"
              class="button button--small"
              disabled=${searching || !spec.query.trim()}
              onClick=${search}
            >
              ${searching ? "Searching…" : "Search"}
            </button>
            ${searchError && html`<p class="modal__error">${searchError}</p>`}
            ${
              spec.noCatalog &&
              html`<p class="plan__note">
                This source has no searchable catalog - enter the identifier
                directly below.
              </p>`
            }
            ${
              spec.matches &&
              (spec.matches.length === 0
                ? html`<p class="empty-state__hint">
                    No game in the catalog matches that name.
                  </p>`
                : html`
                    <ul class="setup-add__matches">
                      ${spec.matches.map(
                        (m) => html`
                          <li key=${m.identifier}>
                            <button
                              type="button"
                              class="button button--small ${spec.identifier === m.identifier ? "button--primary" : ""}"
                              onClick=${() => pickMatch(m)}
                            >
                              ${m.name}
                            </button>
                          </li>
                        `,
                      )}
                    </ul>
                  `)
            }
          </div>
        `
      }

      <label class="plan__control">
        Identifier with that source
        <input
          type="text"
          name="add-identifier"
          placeholder=${identifierHints[spec.sourceID]?.placeholder}
          value=${spec.identifier}
          onInput=${(e) => patch({ identifier: e.currentTarget.value, gameID: undefined })}
        />
      </label>
      ${
        identifierHints[spec.sourceID] &&
        html`<p class="empty-state__hint setup-add__identifier-hint">
          ${identifierHints[spec.sourceID].hint}
        </p>`
      }
      ${errorFor("identifier") && html`<p class="modal__error">${errorFor("identifier")}</p>`}

      <details
        class="setup-add__advanced"
        data-testid="setup-add-advanced"
        open=${Boolean(errorFor("game_id"))}
      >
        <summary>Advanced</summary>
        <label class="plan__control">
          Game id
          <input
            type="text"
            name="add-game-id"
            placeholder=${detectedRow ? detectedRow.slug : "derived automatically if left blank"}
            value=${spec.gameID ?? ""}
            onInput=${(e) =>
              patch({ gameID: e.currentTarget.value || undefined })}
          />
        </label>
        <p class="empty-state__hint">
          The local games.yaml key (--game-id). Set automatically by a catalog
          match above; override it here to choose it by hand.
        </p>
        ${errorFor("game_id") && html`<p class="modal__error">${errorFor("game_id")}</p>`}
      </details>

      ${
        (sources ?? []).length > 1 &&
        html`
          <details class="setup-add__extra-sources">
            <summary>Also map other sources (optional)</summary>
            <p class="empty-state__hint">
              A game can draw mods from more than one source. The identifier may
              be empty - that is how a directory source is normally configured.
            </p>
            <${SourcesMapEditor}
              sources=${(sources ?? []).filter((s) => s.id !== spec.sourceID)}
              value=${spec.extraSources}
              disabled=${busy}
              onChange=${(map) => patch({ extraSources: map })}
            />
          </details>
        `
      }

      <label class="plan__control">
        Display name
        <input
          type="text"
          name="add-name"
          value=${spec.name}
          onInput=${(e) => patch({ name: e.currentTarget.value })}
        />
      </label>
      ${errorFor("name") && html`<p class="modal__error">${errorFor("name")}</p>`}
      ${
        detectedRow
          ? html`
              <div class="plan__control">
                <span>Install path</span>
                <p class="mono" data-testid="add-install-path-readonly">
                  ${detectedRow.install_path}
                </p>
              </div>
            `
          : html`
              <label class="plan__control">
                Install path
                <input
                  type="text"
                  name="add-install-path"
                  value=${spec.installPath}
                  onInput=${(e) => patch({ installPath: e.currentTarget.value })}
                />
              </label>
              ${errorFor("install_path") && html`<p class="modal__error">${errorFor("install_path")}</p>`}
            `
      }

      <label class="plan__control">
        Mod path
        <span class="empty-state__hint"
          >(${detectedRow ? "guessed" : "default"}: install path +
          "/mods")</span
        >
        <input
          type="text"
          name="add-mod-path"
          value=${spec.modPath}
          placeholder=${modPathPlaceholder}
          onInput=${(e) => patch({ modPath: e.currentTarget.value })}
        />
      </label>
      ${errorFor("mod_path") && html`<p class="modal__error">${errorFor("mod_path")}</p>`}
      ${
        formError &&
        html`<p class="modal__error">
          ${formError}
          ${
            staleDetected &&
            html`${" "}<button
                type="button"
                class="button button--small"
                data-action="rescan-detected"
                onClick=${rescan}
              >
                Rescan
              </button>`
          }
        </p>`
      }

      <button
        type="submit"
        class="button button--primary"
        data-action="add-game"
        disabled=${busy || !spec.sourceID || !spec.identifier || !spec.name || !installPathReady}
      >
        ${busy ? "Adding…" : "Add game"}
      </button>
    </form>
  `;
}
