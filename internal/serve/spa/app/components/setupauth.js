// setupauth.js - the Setup page's Auth section (issue 333): per
// auth-capable source's status, log in, log out - and the orphaned-token
// remedy app.AuthStatusReport already carries.
//
// SECRET HANDLING mirrors the backend's own rule (api_auth.go's file
// comment): the typed key never leaves the password input except in the
// POST body itself, and nothing here ever reads report.sources[].api_key
// (the wire has no such member). Since #79 a STORED key is encrypted at
// rest and never reaches the wire at all, so what comes back for one is
// key_fingerprint - the first 8 hex of its SHA-256 - and key_masked
// appears only for a key read from the environment, which lmm holds in the
// clear regardless. keyLabel below renders whichever the row has. The field is
// cleared on SUCCESS, and on a 502 (the check could not run at all -
// nothing here to fix by editing the key); a 400 (the VALIDATOR's own
// verdict - the key itself was wrong) keeps the typed value instead, so a
// single-character typo in a long key does not cost retyping the whole
// thing (first-run readiness item 6, unit7 gate review). Either way, the
// field is a live DOM property no rendered markup ever carries the value
// of - keeping it visible only in the one input the user is looking at
// is not the same as it appearing anywhere ELSE in the page.

import { html, useEffect, useState } from "../render.js";
import { ApiError, getAuthStatus, authLogin, authLogout } from "../api.js";

/** How a credential is named on screen (#79). A stored key is encrypted at
 * rest, so the wire carries only its fingerprint; a key read from the
 * environment still carries the masked form lmm has always shown, because
 * that one is in the process environment either way. A row that would not
 * decrypt has neither, and says so.
 *
 * This is the per-ROW condition only. A key file the server cannot use at
 * all (missing, wrong mode, malformed) is about every credential at once,
 * so GET /api/v1/auth fails rather than answering a report full of
 * unreadable rows, and the section renders that message - which names the
 * file and the remedy - in place of the list (review 2). */
function keyLabel(row) {
  if (row.unreadable) return "unreadable";
  return row.key_masked || row.key_fingerprint || "";
}

export function SetupAuth() {
  const [report, setReport] = useState(null);
  const [error, setError] = useState(null);

  async function reload() {
    try {
      setReport(await getAuthStatus());
      setError(null);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  useEffect(() => {
    reload();
  }, []);

  if (error) {
    return html`
      <div class="empty-state empty-state--error">
        <p>Couldn't load authentication status: ${error}</p>
        <button type="button" class="button button--small" onClick=${reload}>
          Retry
        </button>
      </div>
    `;
  }
  if (report === null) {
    return html`<p class="app-booting">Loading your sources…</p>`;
  }

  const sources = report.sources ?? [];
  const orphaned = report.orphaned ?? [];

  return html`
    <div class="setup-section" data-testid="setup-auth">
      <ul class="setup-auth__list">
        ${sources.map(
          (s) =>
            html`<${AuthSourceRow}
              key=${s.id}
              source=${s}
              onChanged=${reload}
            />`,
        )}
      </ul>

      ${
        orphaned.length > 0 &&
        html`
          <section class="plan__section">
            <h3 class="plan__heading plan__heading--warn">
              Orphaned credentials (${orphaned.length})
            </h3>
            <ul class="setup-auth__list">
              ${orphaned.map(
                (o) =>
                  html`<${OrphanedRow}
                    key=${o.id}
                    token=${o}
                    onChanged=${reload}
                  />`,
              )}
            </ul>
          </section>
        `
      }
    </div>
  `;
}

function AuthSourceRow({ source, onChanged }) {
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  // app.AuthStatusReport.RestartRequired (issue 334), read off the
  // MUTATION's own response: the credential is stored, but the running
  // server did not pick it up (its live re-key could not get the mutation
  // gate in time, or the source could not be rebuilt) and will keep using
  // the old one until restarted. It is kept in row state rather than read
  // from `source`, because the reload that follows is a plain GET, which
  // reports no such event and would erase the fact a moment after it
  // happened.
  const [restartRequired, setRestartRequired] = useState(false);

  async function login(e) {
    e.preventDefault();
    const key = apiKey;
    setBusy(true);
    setError(null);
    try {
      const report = await authLogin(source.id, key);
      setRestartRequired(Boolean(report?.restart_required));
      // Cleared only on success - a 400 keeps the typed value (see this
      // file's own SECRET HANDLING comment, first-run readiness item 6).
      setApiKey("");
      await onChanged();
    } catch (err) {
      // 400 (the validator's own verdict, worth fixing and resubmitting -
      // keep the value) vs 502 (the check never ran at all - nothing here
      // to fix by editing the key, so clear it like every other failure).
      if (!(err instanceof ApiError) || err.status !== 400) {
        setApiKey("");
      }
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function logout() {
    setBusy(true);
    setError(null);
    try {
      const report = await authLogout(source.id);
      setRestartRequired(Boolean(report?.restart_required));
      await onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return html`
    <li class="setup-auth__row" data-source=${source.id}>
      <span class="setup-auth__name">${source.name}</span>
      <span class="setup-auth__status">
        ${
          source.authenticated
            ? html`<span class="badge badge--good">authenticated</span>${" "}
                <span class="mono">${keyLabel(source)}</span>${" "}
                ${source.via && html`<span class="empty-state__hint">via ${source.via}</span>`}`
            : html`<span class="badge">not authenticated</span>`
        }
        ${
          source.unreadable &&
          html`<span class="empty-state__hint"
            >a stored credential exists but could not be decrypted — log in
            again</span
          >`
        }
      </span>
      ${
        source.authenticated
          ? html`
              <button
                type="button"
                class="button button--small"
                disabled=${busy}
                onClick=${logout}
              >
                Log out
              </button>
            `
          : html`
              <form class="setup-auth__login" onSubmit=${login}>
                <input
                  type="password"
                  autocomplete="off"
                  aria-label=${`API key for ${source.name}`}
                  placeholder="API key"
                  value=${apiKey}
                  disabled=${busy}
                  onInput=${(e) => setApiKey(e.currentTarget.value)}
                />
                <button
                  type="submit"
                  class="button button--small button--primary"
                  disabled=${busy || !apiKey}
                >
                  ${busy ? "Checking…" : "Log in"}
                </button>
                ${
                  // M-1/D-3: this was a PLACEHOLDER, in a ~190px field on a
                  // ~900px row - visibly truncated to "or set NEXUSMODS_API_KE"
                  // and gone entirely the moment the user typed. It is the
                  // only place the UI names the variable, and the README
                  // says it is shown beside the field, so it is text now.
                  source.env_var &&
                  html`<span class="setup-auth__env-var empty-state__hint"
                    >or set <span class="mono">${source.env_var}</span></span
                  >`
                }
              </form>
            `
      }
      ${error && html`<p class="modal__error">${error}</p>`}
      ${
        restartRequired &&
        html`<p class="setup-auth__restart" data-testid="auth-restart-required">
          Saved, but this server is still using the previous credential —
          restart <span class="mono">lmm serve</span> to pick it up.
        </p>`
      }
    </li>
  `;
}

/** OrphanedRow offers the remedy AuthStatusReport.Orphaned exists for: a
 * stored token belonging to a source that no longer declares auth (or is no
 * longer registered), removed the same way a normal logout is. */
function OrphanedRow({ token, onChanged }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      await authLogout(token.id);
      await onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return html`
    <li class="setup-auth__row" data-source=${token.id}>
      <span class="setup-auth__name">${token.id}</span>
      <span class="mono">${keyLabel(token)}</span>
      <span class="empty-state__hint">${token.reason}</span>
      <button
        type="button"
        class="button button--small button--danger"
        disabled=${busy}
        onClick=${remove}
      >
        Remove
      </button>
      ${error && html`<p class="modal__error">${error}</p>`}
    </li>
  `;
}
