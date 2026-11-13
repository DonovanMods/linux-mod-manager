// pluginlistresult.js - what a finished deploy or purge did to the game's
// plugin list (issue 616): core.PluginListResult, the result's `plugins`
// field. One line saying whether Plugins.txt was written, put back or left
// alone, then the findings - the ones the game will not survive (a cycle, a
// missing master, too many active plugins) marked as errors. The CLI prints
// the same line and the same sentences (cmd/lmm/plugins_render.go).

import { html } from "../render.js";

/**
 * pluginListLine is the result's one-line summary, in the CLI's words.
 */
export function pluginListLine(result) {
  if (!result || typeof result.status !== "string") return "";
  const plugins = Number.isSafeInteger(result.plugins) ? result.plugins : 0;
  const active = Number.isSafeInteger(result.active) ? result.active : 0;
  const external = Number.isSafeInteger(result.external) ? result.external : 0;
  switch (result.status) {
    case "written":
    case "unchanged": {
      const kept =
        external > 0 ? ` (${external} kept that lmm did not deploy)` : "";
      return `Plugins.txt ${result.status}: ${plugins} plugin(s), ${active} active${kept}`;
    }
    case "restored":
      return "Plugins.txt restored to the list you had before lmm wrote it";
    case "removed":
      return "Plugins.txt removed (there was none before lmm wrote it)";
    default:
      return "Plugins.txt not written";
  }
}

/** pluginFindings is the result's findings, the well-formed ones only. */
export function pluginFindings(result) {
  if (!Array.isArray(result?.findings)) return [];
  return result.findings.filter(
    (f) => typeof f?.message === "string" && f.message !== "",
  );
}

/**
 * PluginListResult renders a result's plugin list, or nothing for a result
 * without one (every game whose adapter manages no plugin list). An
 * unchanged list with nothing to say stays quiet: it is the common case.
 */
export function PluginListResult({ result }) {
  const line = pluginListLine(result);
  if (!line) return null;
  const findings = pluginFindings(result);
  if (result.status === "unchanged" && findings.length === 0) return null;
  return html`
    <div class="job-progress__explainer" data-testid="plugin-list-result">
      <p data-plugin-status=${result.status}>${line}</p>
      ${
        result.list_file &&
        html`<p class="mono plugin-list__path">${result.list_file}</p>`
      }
      ${
        findings.length > 0 &&
        html`<ul class="tray__skips" data-testid="plugin-list-findings">
          ${findings.map(
            (f, i) =>
              html`<li
                key=${`${f.code}:${i}`}
                class="tray__skip plugin-list__finding plugin-list__finding--${f.severity === "error" ? "error" : "warning"}"
                data-code=${f.code}
              >
                ${f.severity === "error" ? "Error: " : "Warning: "}${f.message}
              </li>`,
          )}
        </ul>`
      }
    </div>
  `;
}
