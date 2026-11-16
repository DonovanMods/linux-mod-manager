// setupupdates.js - the Setup page's Updates section (issue 586): what lmm
// knows about its own releases, and the "Update automatically" switch.
//
// Everything here is GET /api/v1/updates/lmm (core.ReleaseStatus with its
// auto_update), the server's cached answer - nothing on this page asks
// GitHub. The switch writes config.yaml's auto_update through PUT
// /api/v1/settings/auto-update, a single-step write with nothing to preview,
// and re-renders from the status that answer carries.
//
// The switch is disabled, with the reason beside it, where an automatic
// update can never happen: a package's lmm (its package manager updates it)
// and a development build. Where it is on but the daily release check is
// off, it says that nothing triggers an update from this machine's commands
// or this server - only lmm-update.timer still would.
//
// Its "Background service" row (issue 649) is GET /api/v1/service, `lmm
// service status --json`'s document: the lmm binary lmm-serve.service runs
// and that binary's version, with a warning when it is not the binary this
// server runs. It is read-only, as the CLI is where the service is
// installed, restarted and switched; the warning names that command. A
// server that does not report its service (204, no document) shows no row.

import { html, useEffect, useState } from "../render.js";
import { getLmmRelease, getServiceStatus, putAutoUpdate } from "../api.js";

/** INSTALL_LABELS names core.InstallKind for a person. */
const INSTALL_LABELS = {
  "self-managed":
    "Self-managed (a release download, go install, " +
    "or make install of a release tag)",
  package: "Package",
  dev: "Development build",
};

function formatWhen(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
}

function installLabel(install) {
  if (!install?.method) return "Unknown";
  if (install.method === "package") {
    return `Package: ${install.package} (${install.manager})`;
  }
  return INSTALL_LABELS[install.method] ?? install.method;
}

function latestLabel(status) {
  if (status.dev) return "Not checked for a development build";
  if (status.latest) {
    const when = formatWhen(status.checked_at);
    return when ? `${status.latest} (checked ${when})` : status.latest;
  }
  if (status.checked_at && !status.check_error) return "None published";
  return "Not checked yet";
}

/** serveUnit is lmm-serve.service's row of a core.ServiceReport. */
function serveUnit(report) {
  return report?.units?.find((u) => u.name === "lmm-serve.service");
}

/** serviceLabel is the "Background service" row's text: the binary the unit
 * runs, or why there is none to name. */
function serviceLabel(service) {
  if (service.error) return `Not available: ${service.error}`;
  const unit = serveUnit(service.report);
  if (!unit?.installed) {
    return "Not installed (lmm service install runs this web UI in the background)";
  }
  const state = unit.active ? "running" : "not running";
  if (!unit.binary) return `lmm-serve.service, ${state}`;
  if (!unit.binary.path) {
    return `lmm-serve.service runs a program lmm cannot name (a drop-in resets its ExecStart; see systemctl --user cat lmm-serve), ${state}`;
  }
  return `lmm-serve.service runs ${unit.binary.path} (${unit.binary.version}), ${state}`;
}

/** ServiceMismatch warns that lmm-serve.service runs another lmm binary
 * than this server, with the command that points it at the right one - or,
 * for a package's unit, which lmm never rewrites, how to run another lmm. */
function ServiceMismatch({ report }) {
  const unit = serveUnit(report);
  const running = report?.running;
  if (!unit?.binary_mismatch || !unit.binary || !running) return null;
  const remedy =
    unit.origin === "package"
      ? "It is your lmm package's unit, which lmm does not rewrite: override its ExecStart with systemctl --user edit lmm-serve."
      : "Run lmm service restart --switch with the lmm you want it to run, to point the service at that binary and restart it.";
  return html`<div
    class="service-binary-warning"
    role="note"
    data-testid="setup-updates-service-mismatch"
  >
    <p class="service-binary-warning__text">
      lmm-serve.service runs ${unit.binary.path} (${unit.binary.version}); this
      web UI runs ${running.path} (${running.version}). ${remedy}
    </p>
  </div>`;
}

export function SetupUpdates() {
  const [status, setStatus] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // service is GET /api/v1/service's answer: {report} or {error}; null
  // while it loads and for a server that does not report its service.
  const [service, setService] = useState(null);

  useEffect(() => {
    let live = true;
    getLmmRelease()
      .then((s) => live && setStatus(s))
      .catch((e) => live && setError(e.message || String(e)));
    getServiceStatus()
      .then((report) => live && report && setService({ report }))
      .catch((e) => live && setService({ error: e.message || String(e) }));
    return () => {
      live = false;
    };
  }, []);

  async function toggle(enabled) {
    setBusy(true);
    setError("");
    try {
      setStatus(await putAutoUpdate(enabled));
    } catch (e) {
      setError(e.message || String(e));
    } finally {
      setBusy(false);
    }
  }

  if (!status) {
    return html`
      <div
        class="form-section form-section--measure setup-section"
        data-testid="setup-updates"
      >
        ${
          error
            ? html`<p class="app-error">${error}</p>`
            : html`<p class="empty-state__hint">Loading…</p>`
        }
      </div>
    `;
  }

  const auto = status.auto_update;
  const applicable = Boolean(auto?.applicable);
  const enabled = Boolean(auto?.enabled);
  const reasonID = "setup-updates-reason";
  const last = auto?.last;
  const failure = auto?.failure;

  return html`
    <div
      class="form-section form-section--measure setup-section"
      data-testid="setup-updates"
      data-applicable=${applicable ? "true" : "false"}
    >
      <dl class="doc__pairs">
        <div class="doc__pair">
          <dt class="doc__key">Installed version</dt>
          <dd class="doc__value" data-testid="setup-updates-current">
            ${status.installed ? `${status.current} (${status.installed} installed; restart lmm serve to use it)` : status.current}
          </dd>
        </div>
        <div class="doc__pair">
          <dt class="doc__key">Latest release</dt>
          <dd class="doc__value" data-testid="setup-updates-latest">
            ${latestLabel(status)}
            ${
              status.release_url &&
              html` <a
                href=${status.release_url}
                target="_blank"
                rel="noopener noreferrer"
                >Release notes</a
              >`
            }
          </dd>
        </div>
        <div class="doc__pair">
          <dt class="doc__key">Install method</dt>
          <dd class="doc__value" data-testid="setup-updates-install">
            ${installLabel(status.install)}
          </dd>
        </div>
        <div class="doc__pair">
          <dt class="doc__key">Release check</dt>
          <dd class="doc__value" data-testid="setup-updates-check">
            ${
              status.check_enabled
                ? "On: lmm asks GitHub for a new release once a day"
                : "Off (update_check: false in config.yaml)"
            }
          </dd>
        </div>
        ${
          service &&
          html`<div class="doc__pair">
            <dt class="doc__key">Background service</dt>
            <dd class="doc__value" data-testid="setup-updates-service">
              ${serviceLabel(service)}
            </dd>
          </div>`
        }
        ${
          last &&
          html`<div class="doc__pair">
            <dt class="doc__key">Last automatic update</dt>
            <dd class="doc__value" data-testid="setup-updates-last">
              ${last.from} to ${last.to}, ${formatWhen(last.at)}
            </dd>
          </div>`
        }
        ${
          failure &&
          html`<div class="doc__pair">
            <dt class="doc__key">Last failure</dt>
            <dd class="doc__value" data-testid="setup-updates-failure">
              Updating to ${failure.version} failed ${formatWhen(failure.at)}:
              ${failure.error}
            </dd>
          </div>`
        }
      </dl>
      ${service?.report && html`<${ServiceMismatch} report=${service.report} />`}
      <label class="form-check">
        <input
          type="checkbox"
          role="switch"
          data-action="toggle-auto-update"
          checked=${applicable && enabled}
          disabled=${busy || !applicable}
          aria-describedby=${reasonID}
          onChange=${(e) => toggle(e.currentTarget.checked)}
        />
        Update automatically
      </label>
      <p class="form-hint" id=${reasonID} data-testid="setup-updates-reason">
        ${
          !applicable
            ? auto?.reason ||
              "lmm cannot tell how it was installed, so it does not update itself."
            : !enabled
              ? "Off: lmm tells you about a new release but leaves installing it to you (auto_update: false in config.yaml)."
              : status.check_enabled
                ? "On: when the daily check finds a new release, lmm verifies its signature and installs it by itself - after a command in a terminal, or from lmm serve, which then restarts if it runs as lmm-serve.service."
                : "On, but the release check is off, so nothing here learns of a new release: only lmm-update.timer (lmm service install --auto-update) still updates lmm."
        }
      </p>
      ${error && html`<p class="app-error">${error}</p>`}
    </div>
  `;
}
