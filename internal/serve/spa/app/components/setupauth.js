// setupauth.js - the Setup page's Auth section (issue 333): per
// auth-capable source's status, log in, log out - and the orphaned-token
// remedy app.AuthStatusReport already carries.
//
// SECRET HANDLING mirrors the backend's own rule (api_auth.go's file
// comment): the typed key never leaves the password input except in the
// POST body itself. The field is cleared immediately after every submit -
// success or failure - and nothing here ever reads report.sources[].api_key
// (the wire has no such member; only key_masked comes back). A key that a
// validator refuses, or that could not be checked at all (502), is left in
// no state here to re-display - only the field, already blank, and the
// message beside it.

import { html, useEffect, useState } from "../render.js";
import { ApiError, getAuthStatus, authLogin, authLogout } from "../api.js";

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
    return html`<p class="app-booting">Loading&#8230;</p>`;
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
            <p class="plan__heading plan__heading--warn">
              Orphaned credentials (${orphaned.length})
            </p>
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

  async function login(e) {
    e.preventDefault();
    const key = apiKey;
    setApiKey("");
    setBusy(true);
    setError(null);
    try {
      await authLogin(source.id, key);
      await onChanged();
    } catch (err) {
      // 400 (the validator's own verdict) vs 502 (the check never ran) -
      // the message already distinguishes them; this UI adds no branch of
      // its own beyond rendering it (api_auth.go's own doc comment).
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function logout() {
    setBusy(true);
    setError(null);
    try {
      await authLogout(source.id);
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
                <span class="mono">${source.key_masked}</span>${" "}
                ${source.via && html`<span class="empty-state__hint">via ${source.via}</span>`}`
            : html`<span class="badge">not authenticated</span>`
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
                  placeholder=${source.env_var ? `or set ${source.env_var}` : "API key"}
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
              </form>
            `
      }
      ${error && html`<p class="modal__error">${error}</p>`}
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
      <span class="mono">${token.key_masked}</span>
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
