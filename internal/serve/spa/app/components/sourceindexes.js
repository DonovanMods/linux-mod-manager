// sourceindexes.js - the Setup page's local search indexes (issue 410,
// design §4.2): the web twin of `lmm source index --all`, `--refresh` and
// `lmm source index prune`.
//
// A source that searches a LOCAL copy of its catalogue (Thunderstore) keeps
// one index per community under the cache directory. This section lists
// every one on disk - and every one a game maps that has not been built -
// with its size, its age and the games that use it; rebuilds one on
// demand; and prunes the ones nothing needs, previewing first and removing
// only what the preview showed (the prune's `only` binding).
//
// Every call is a settings-class single-step route (api_source_index.go):
// nothing here is a job, because a rebuild is a few seconds and a prune
// removes files that are rebuilt on the next search.

import { html, useEffect, useState } from "../render.js";
import {
  ApiError,
  listSourceIndexes,
  refreshSourceIndex,
  pruneSourceIndexes,
} from "../api.js";
import { formatBytes } from "../progress.js";
import { countdown, relativeTime } from "../relativetime.js";
import { codeSpans } from "../errortext.js";
import { ErrorDetails } from "./errordetails.js";
import { forgetIndexListing } from "../indexnotice.js";

/** describeError turns a rejection into the message/details pair a row
 * renders. */
function describeError(err) {
  if (err instanceof ApiError)
    return { message: err.message, details: err.details };
  return { message: String(err), details: null };
}

/**
 * SourceIndexes renders nothing until the listing has loaded, and nothing
 * at all for an installation where no source keeps an index and no game
 * maps one - the section is about a capability most games do not use.
 *
 * sources (optional) is the Sources card's own listing (app.SourceInfo,
 * setupsources.js) - it already has each source's display name, so a hold
 * (which the wire names only by source id) can say "Thunderstore" rather
 * than "thunderstore" without a second fetch.
 */
export function SourceIndexes({ sources } = {}) {
  const [listing, setListing] = useState(null);
  const [error, setError] = useState(null);
  const nameFor = (id) => sources?.find((s) => s.id === id)?.name || id;

  async function reload() {
    forgetIndexListing();
    try {
      setListing(await listSourceIndexes());
      setError(null);
    } catch (err) {
      setError(describeError(err).message);
    }
  }

  useEffect(() => {
    reload();
  }, []);

  if (error) {
    return html`
      <div class="setup-section" data-testid="source-indexes">
        <h2 class="section-header">Local search indexes</h2>
        <p class="modal__error">Couldn't load the indexes: ${error}</p>
      </div>
    `;
  }
  if (listing === null) return null;
  const indexes = listing.indexes ?? [];
  if (indexes.length === 0 && (listing.warnings ?? []).length === 0) {
    return null;
  }

  const total = indexes.reduce((sum, e) => sum + (e.cached ? e.bytes : 0), 0);

  return html`
    <div class="setup-section" data-testid="source-indexes">
      <h2 class="section-header">Local search indexes</h2>
      <p class="empty-state__hint">
        Thunderstore has no search endpoint, so lmm keeps a copy of each
        community's catalogue and searches that. It is built on the first search
        and refreshed every six hours; refresh it here when a package published
        in the last few hours does not show up. Everything here is rebuilt on
        demand, so pruning never loses anything you cannot get back.
      </p>
      ${(listing.warnings ?? []).map(
        (w) => html`<p key=${w} class="plan__note--warn">${w}</p>`,
      )}
      <${IndexHolds} holds=${listing.holds ?? []} nameFor=${nameFor} />
      <table class="setup-table">
        <thead>
          <tr>
            <th>Source</th>
            <th>Index</th>
            <th>Packages</th>
            <th>Size</th>
            <th>Updated</th>
            <th>Used by</th>
            <th class="setup-table__actions"></th>
          </tr>
        </thead>
        <tbody>
          ${indexes.map(
            (entry) => html`
              <${IndexRow}
                key=${`${entry.source}/${entry.game}`}
                entry=${entry}
                hold=${(listing.holds ?? []).find(
                  (h) =>
                    h.game &&
                    h.source === entry.source &&
                    h.game === entry.game,
                )}
                onChanged=${reload}
              />
            `,
          )}
        </tbody>
      </table>
      <p class="empty-state__hint" data-testid="source-indexes-total">
        Total on disk: ${formatBytes(total) || "0 B"}
      </p>
      <${PrunePanel} onChanged=${reload} />
    </div>
  `;
}

/**
 * IndexHolds is issue 436's holds in force (T3 review F10), shown WITHOUT a
 * request: a source that will not be asked at all - or, with a game, will
 * not be asked about that one index - before its own retry_at, and why. It
 * is a role="status" region so a screen reader announces it the moment the
 * listing loads, the same way a completed refresh's outcome does.
 */
function IndexHolds({ holds, nameFor }) {
  if (holds.length === 0) return null;
  return html`
    <div
      role="status"
      class="source-indexes__holds"
      data-testid="source-index-holds"
    >
      ${holds.map(
        (h) => html`
          <p
            key=${`${h.source}/${h.game ?? ""}`}
            class="plan__note--warn"
            data-hold=${h.game || h.source}
          >
            ${holdLine(h, nameFor(h.source))}
          </p>
        `,
      )}
    </div>
  `;
}

/** holdLine is one source.Hold as a sentence, mirroring
 * source.RetryLaterError.Error()'s own wording ("not asking ... again
 * until ...: ...") but capitalized for a standalone line, in local time
 * (clockTime's own reasoning: the clock the reader has), plus the
 * relative-time countdown so a reader does not have to do the subtraction
 * themselves. */
function holdLine(hold, name) {
  const clock = holdClock(hold.retry_at);
  const wait = countdown(hold.retry_at);
  const about = hold.game ? ` about ${hold.game}` : "";
  const when = wait ? `${clock} (${wait})` : clock;
  return `Not asking ${name}${about} again until ${when}: ${hold.reason}`;
}

/** holdClock is a hold's end on the reader's clock: the time alone within
 * the next twelve hours, the date as well further off - the CLI's rule. */
function holdClock(value, now = Date.now()) {
  const at = new Date(value);
  if (Math.abs(at.getTime() - now) < 12 * 60 * 60 * 1000) {
    return at.toLocaleTimeString();
  }
  return at.toLocaleString();
}

/**
 * IndexRow is one index. Its Refresh button rebuilds it for the first game
 * that maps it - the index route is game-scoped because an index IS a
 * game's mapping - and an index no game uses has nothing to refresh for.
 */
function IndexRow({ entry, hold, onChanged }) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState(null);
  const [outcome, setOutcome] = useState("");
  const usedBy = entry.mapped_by ?? [];
  const game = usedBy[0];

  async function refresh() {
    setBusy(true);
    setFailure(null);
    setOutcome("");
    try {
      const report = await refreshSourceIndex(entry.source, game, true);
      setOutcome(reportText(report));
      await onChanged();
    } catch (err) {
      setFailure(describeError(err));
    } finally {
      setBusy(false);
    }
  }

  const updated = entry.cached ? relativeTime(entry.fetched_at) : "";
  return html`
    <tr
      data-index=${entry.game}
      data-source=${entry.source}
      data-cached=${entry.cached ? "true" : "false"}
    >
      <td class="mono">${entry.source}</td>
      <td class="mono">${entry.game}</td>
      <td data-testid="index-packages">
        ${entry.cached ? String(entry.packages) : "—"}
      </td>
      <td data-testid="index-size">
        ${entry.cached ? formatBytes(entry.bytes) || "0 B" : "—"}
      </td>
      <td data-testid="index-updated">
        ${entry.cached ? updated || "unknown" : "not built yet"}
        ${
          hold &&
          html`
            <span class="empty-state__hint" data-testid="index-held-until">
              held until ${holdClock(hold.retry_at)}
            </span>
          `
        }
      </td>
      <td>${usedBy.length > 0 ? usedBy.join(", ") : "—"}</td>
      <td class="setup-table__actions">
        ${
          game &&
          html`
            <button
              type="button"
              class="button button--small"
              data-action="refresh-index"
              disabled=${busy}
              onClick=${refresh}
            >
              ${busy ? "Refreshing…" : entry.cached ? "Refresh index" : "Build index"}
            </button>
          `
        }
        ${
          entry.keep_reason &&
          html`
            <p class="empty-state__hint" data-testid="index-keep-reason">
              Prune keeps this: ${entry.keep_reason}
            </p>
          `
        }
        ${
          outcome &&
          html`<p class="plan__note" data-testid="index-outcome">${outcome}</p>`
        }
        ${
          failure &&
          html`
            <div class="modal__error" data-testid="index-error">
              <p>${codeSpans(failure.message)}</p>
              <${ErrorDetails} details=${failure.details} />
            </div>
          `
        }
      </td>
    </tr>
  `;
}

/** reportText says what a refresh did, in core.IndexReport's own terms. */
function reportText(report) {
  const size = formatBytes(report.bytes) || "0 B";
  switch (report.status) {
    case "built":
      return report.changed
        ? `Updated: ${report.packages} packages, ${size}.`
        : `Rebuilt, unchanged: ${report.packages} packages, ${size}.`;
    case "stale":
      return `Could not refresh; still using the copy on disk. ${(report.warnings ?? []).join("; ")}`;
    default:
      return `Already current: ${report.packages} packages, ${size}.`;
  }
}

/**
 * PrunePanel is the prune: Preview asks the server what a prune WOULD
 * remove (optionally including indexes in use), and Remove runs it bound to
 * exactly that preview's removal keys - so nothing appears in the result
 * that the user did not see in the preview.
 */
function PrunePanel({ onChanged }) {
  const [preview, setPreview] = useState(null);
  const [all, setAll] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState(null);
  // confirmedOnly is exactly what the last confirm() sent as `only` - the
  // keys PruneProblems needs to tell "the preview said remove and the run
  // came back keep" (F11) apart from an entry that was never up for
  // removal in the first place.
  const [confirmedOnly, setConfirmedOnly] = useState([]);
  const [error, setError] = useState(null);

  async function runPreview(withAll) {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setPreview(await pruneSourceIndexes({ all: withAll, dryRun: true }));
    } catch (err) {
      setError(describeError(err).message);
    } finally {
      setBusy(false);
    }
  }

  async function confirm() {
    const only = removalKeys(preview);
    setBusy(true);
    setError(null);
    try {
      setResult(await pruneSourceIndexes({ all, only }));
      setConfirmedOnly(only);
      setPreview(null);
      // The next prune starts from the safe default again: "also remove
      // indexes a game uses" is a decision for one prune, not a setting.
      setAll(false);
      await onChanged();
    } catch (err) {
      setError(describeError(err).message);
    } finally {
      setBusy(false);
    }
  }

  if (!preview) {
    return html`
      <div class="setup-section__actions">
        <button
          type="button"
          class="button button--small"
          data-action="prune-indexes"
          disabled=${busy}
          onClick=${() => runPreview(all)}
        >
          ${busy ? "Checking…" : "Prune unused indexes…"}
        </button>
        ${
          result &&
          html`<span class="plan__note" data-testid="prune-result">
            Removed ${result.removed} index(es), freeing
            ${" "}${formatBytes(result.freed_bytes) || "0 B"}.
          </span>`
        }
        ${error && html`<span class="modal__error">${error}</span>`}
      </div>
      <${PruneProblems} report=${result} only=${confirmedOnly} />
    `;
  }

  const removals = (preview.entries ?? []).filter((e) => e.action === "remove");
  const kept = (preview.entries ?? []).filter((e) => e.action !== "remove");
  return html`
    <div class="source-indexes__preview" data-testid="prune-preview">
      <label class="plan__control plan__control--inline">
        <input
          type="checkbox"
          name="prune-all"
          checked=${all}
          disabled=${busy}
          onChange=${(e) => {
            const next = e.currentTarget.checked;
            setAll(next);
            runPreview(next);
          }}
        />
        Also remove indexes a game uses (they are rebuilt on its next search)
      </label>
      ${
        removals.length === 0
          ? html`<p class="plan__note">Nothing to remove.</p>`
          : html`
              <p class="plan__note">
                Would remove ${removals.length} index(es), freeing
                ${" "}${formatBytes(preview.freed_bytes) || "0 B"}:
              </p>
              <ul class="source-indexes__preview-list">
                ${removals.map(
                  (e) => html`
                    <li key=${`${e.source}/${e.game}`} data-prune=${e.game}>
                      <span class="mono">${e.game}</span>
                      (${formatBytes(e.bytes) || "0 B"}) — ${e.reason}
                    </li>
                  `,
                )}
              </ul>
            `
      }
      ${
        kept.length > 0 &&
        html`
          <p class="empty-state__hint">Kept:</p>
          <ul class="source-indexes__preview-list">
            ${kept.map(
              (e) => html`
                <li key=${`${e.source}/${e.game}`} data-keep=${e.game}>
                  <span class="mono">${e.game}</span> — ${e.reason}
                </li>
              `,
            )}
          </ul>
        `
      }
      ${(preview.warnings ?? []).map(
        (w) => html`<p key=${w} class="plan__note--warn">${w}</p>`,
      )}
      <div class="setup-section__actions">
        <button
          type="button"
          class="button button--small button--danger"
          data-action="confirm-prune"
          disabled=${busy || removals.length === 0}
          onClick=${confirm}
        >
          ${busy ? "Removing…" : `Remove ${removals.length}`}
        </button>
        <button
          type="button"
          class="button button--small"
          data-action="cancel-prune"
          disabled=${busy}
          onClick=${() => {
            setPreview(null);
            setAll(false);
          }}
        >
          Cancel
        </button>
      </div>
      ${error && html`<p class="modal__error">${error}</p>`}
    </div>
  `;
}

/**
 * PruneProblems is what a finished prune could NOT do: every entry the
 * source refused or failed to remove, with its reason; every entry the
 * preview said it would remove but the confirmed run kept instead - a
 * refresh, or a game mapping it, landed between the two calls (F11); and
 * every source it could not list at all. A prune that removed nothing
 * because of these must never read as a prune that simply had nothing to
 * do.
 *
 * only is the confirmed run's own `only` list (removalKeys of the preview
 * it answered), so "kept anyway" names exactly the entries the preview
 * promised to remove - never an entry that was never up for removal, which
 * `action !== "remove"` alone could not tell apart.
 */
function PruneProblems({ report, only = [] }) {
  if (!report) return null;
  const failed = (report.entries ?? []).filter((e) => e.action === "failed");
  const onlyKeys = new Set(only);
  const keptAnyway = (report.entries ?? []).filter(
    (e) => e.action === "keep" && onlyKeys.has(`${e.source}/${e.game}`),
  );
  const warnings = report.warnings ?? [];
  if (failed.length === 0 && keptAnyway.length === 0 && warnings.length === 0) {
    return null;
  }
  return html`
    <div class="modal__error" data-testid="prune-problems">
      ${
        failed.length > 0 &&
        html`
          <p>Not removed:</p>
          <ul class="source-indexes__preview-list">
            ${failed.map(
              (e) => html`
                <li key=${`${e.source}/${e.game}`} data-failed=${e.game}>
                  <span class="mono">${e.game}</span> — ${e.reason}
                </li>
              `,
            )}
          </ul>
        `
      }
      ${
        keptAnyway.length > 0 &&
        html`
          <ul class="source-indexes__preview-list">
            ${keptAnyway.map(
              (e) => html`
                <li key=${`${e.source}/${e.game}`} data-kept-anyway=${e.game}>
                  <span class="mono">${e.game}</span> was kept: ${e.reason}
                </li>
              `,
            )}
          </ul>
        `
      }
      ${warnings.map((w) => html`<p key=${w}>${w}</p>`)}
    </div>
  `;
}

/** removalKeys is a preview's IndexPruneReport.RemovalKeys, client side:
 * the "source/game" keys of what it would remove. */
function removalKeys(report) {
  return (report?.entries ?? [])
    .filter((e) => e.action === "remove")
    .map((e) => `${e.source}/${e.game}`);
}
