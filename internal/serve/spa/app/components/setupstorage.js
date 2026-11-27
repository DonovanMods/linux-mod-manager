// setupstorage.js - the Setup page's Storage section (issue 678): the mod
// cache's size per game, how much a prune frees, the
// cache.keep_previous_versions setting, and the prune itself.
//
// The status is GET /api/v1/cache (core.CacheStatus, `lmm cache status
// --json`), which reads every cached file's size. The setting is PUT
// /api/v1/settings/cache, a single-step config.yaml write answered with
// the status it changes (`lmm cache keep`). The prune is the "cache_prune"
// plan kind: the confirm modal previews every version it removes with its
// size, as `lmm cache prune --dry-run` lists them, and the job removes
// them. The status is read again when the prune's job finishes.

import { html, useEffect, useRef, useState } from "../render.js";
import { getCacheStatus, putCacheKeep } from "../api.js";
import { formatBytes } from "../progress.js";
import { InlineJob } from "./jobprogress.js";
import { keepSentence } from "./plan_cache_prune.js";
import { oldVersions } from "./planprune.js";

const PRUNE_ORIGIN = "setup:cache-prune";
const PRUNE_ALL_ORIGIN = "setup:cache-prune-all";

/** KEEP_CHOICES are the setting's offered values; -1 is unlimited. */
const KEEP_CHOICES = [0, 1, 2, 3, 4, 5, 6, 8, 10];

function keepLabel(n) {
  if (n < 0) return "Unlimited: never prune";
  if (n === 0) return "0: only installed and referenced versions";
  return n === 1 ? "1 previous version" : `${n} previous versions`;
}

/** bytesOf renders a byte count, "0 B" included. */
function bytesOf(n) {
  return formatBytes(n) || "0 B";
}

/** reclaimLabel is a status row's reclaimable column. */
function reclaimLabel(row) {
  if (row.skipped) return "Unknown";
  if (!(row.reclaimable_versions > 0)) return "Nothing";
  return `${bytesOf(row.reclaimable_bytes)} (${oldVersions(row.reclaimable_versions)})`;
}

/** finishedState is the state of the job an origin last started, or "". */
function finishedState(state, origin) {
  const id = state?.origins?.[origin];
  if (!id) return "";
  return (state.jobsIndex ?? []).find((row) => row.id === id)?.state ?? "";
}

export function SetupStorage({ state, actions, game }) {
  const [status, setStatus] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // chosen is the value the select shows while its write is in flight, so
  // a re-render before the answer never puts the old value back (and a
  // second change event never writes it again); inflight is the same
  // value for the change handler, which may run before that re-render.
  const [chosen, setChosen] = useState(null);
  const inflight = useRef(null);
  const pruneDone = finishedState(state, PRUNE_ORIGIN);
  const pruneAllDone = finishedState(state, PRUNE_ALL_ORIGIN);

  // The status is read on arrival and again once a prune's job has
  // finished (its state leaves "running").
  useEffect(() => {
    if (pruneDone === "running" || pruneAllDone === "running") return;
    let live = true;
    getCacheStatus()
      .then((s) => {
        if (!live) return;
        setStatus(s);
        setError("");
      })
      .catch((e) => live && setError(e.message || String(e)));
    return () => {
      live = false;
    };
  }, [pruneDone, pruneAllDone]);

  async function setKeep(value) {
    const current = inflight.current ?? String(status?.keep_previous_versions);
    if (value === current) return;
    inflight.current = value;
    setChosen(value);
    setBusy(true);
    setError("");
    try {
      setStatus(await putCacheKeep(Number(value)));
    } catch (e) {
      setError(e.message || String(e));
    } finally {
      inflight.current = null;
      setChosen(null);
      setBusy(false);
    }
  }

  function prune(allGames) {
    actions.openPlan({
      kind: "cache_prune",
      origin: allGames ? PRUNE_ALL_ORIGIN : PRUNE_ORIGIN,
      title: allGames
        ? "Prune the cache of every game"
        : "Prune this game's cache",
      confirmLabel: "Prune",
      options: allGames ? { all_games: true } : {},
    });
  }

  if (!status) {
    return html`
      <div
        class="form-section form-section--measure setup-section"
        data-testid="setup-storage"
      >
        ${
          error
            ? html`<p class="app-error">${error}</p>`
            : html`<p class="empty-state__hint">Reading the cache…</p>`
        }
      </div>
    `;
  }

  const keep = status.keep_previous_versions;
  const choices =
    KEEP_CHOICES.includes(keep) || keep < 0
      ? KEEP_CHOICES
      : [...KEEP_CHOICES, keep].sort((a, b) => a - b);
  const games = status.games ?? [];
  const selected = game;
  const selectedRow = games.find(
    (g) => g.game_id === selected || (g.shared_with ?? []).includes(selected),
  );

  return html`
    <div
      class="form-section form-section--measure setup-section"
      data-testid="setup-storage"
    >
      <p class="form-hint">
        lmm keeps the files of the mod versions it installs in its cache, so a
        rollback or a profile switch needs no download. It prunes a mod's old
        versions by itself after an update, a rollback, a profile apply or
        switch, or a collection update changes its version.
      </p>
      <div class="form-field">
        <label class="form-field__label" for="setup-storage-keep">
          Previous versions to keep per mod
        </label>
        <select
          id="setup-storage-keep"
          name="keep_previous_versions"
          data-action="cache-keep"
          value=${chosen ?? String(keep)}
          disabled=${busy}
          aria-describedby="setup-storage-keep-hint"
          onChange=${(e) => setKeep(e.currentTarget.value)}
        >
          ${choices.map((n) => html`<option key=${n} value=${String(n)}>${keepLabel(n)}</option>`)}
          <option value="-1">${keepLabel(-1)}</option>
        </select>
        <p
          class="form-hint"
          id="setup-storage-keep-hint"
          data-testid="setup-storage-rule"
        >
          ${keepSentence(keep)} A referenced version - a rollback target, a
          locked or pinned version, a collection pin, another profile's, a
          snapshot's - is always kept; one among the newest uses a slot. Delete
          a snapshot to let go of the versions only it references.
        </p>
      </div>
      <table class="setup-table" data-testid="setup-storage-table">
        <thead>
          <tr>
            <th>Game</th>
            <th>Size</th>
            <th>Mods</th>
            <th>Versions</th>
            <th>A prune frees</th>
          </tr>
        </thead>
        <tbody>
          ${games.map(
            (g) => html`
              <tr
                key=${g.game_id}
                data-testid="setup-storage-row"
                data-game=${g.game_id}
              >
                <td>
                  ${g.game_name || g.game_id}
                  ${(g.shared_with ?? []).length > 0 && html` (shared with ${g.shared_with.join(", ")})`}
                </td>
                <td>${bytesOf(g.bytes)}</td>
                <td>${g.mods}</td>
                <td>${g.versions}</td>
                <td data-testid="setup-storage-reclaim">${reclaimLabel(g)}</td>
              </tr>
            `,
          )}
        </tbody>
      </table>
      <p class="form-hint" data-testid="setup-storage-total">
        Total:
        ${bytesOf(status.bytes)}${
          status.reclaimable_versions > 0
            ? `, of which a prune frees ${bytesOf(status.reclaimable_bytes)} (${oldVersions(status.reclaimable_versions)}).`
            : "."
        }
      </p>
      ${games
        .filter((g) => g.skipped)
        .map(
          (g) =>
            html`<p class="plan__note plan__note--warn" key=${g.game_id}>
              ${g.game_id}'s cache would not be pruned: ${g.skipped}
            </p>`,
        )}
      <div class="form-actions setup-section__actions">
        ${
          selectedRow &&
          html`<${InlineJob}
            origin=${PRUNE_ORIGIN}
            state=${state}
            actions=${actions}
          >
            <button
              type="button"
              class="button"
              data-action="plan-cache-prune"
              onClick=${() => prune(false)}
            >
              Prune this game…
            </button>
          <//>`
        }
        <${InlineJob}
          origin=${PRUNE_ALL_ORIGIN}
          state=${state}
          actions=${actions}
        >
          <button
            type="button"
            class="button button--primary"
            data-action="plan-cache-prune-all"
            onClick=${() => prune(true)}
          >
            Prune every game…
          </button>
        <//>
      </div>
      ${error && html`<p class="app-error">${error}</p>`}
    </div>
  `;
}
