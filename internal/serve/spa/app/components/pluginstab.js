// pluginstab.js - the reorder modal's Plugins tab (issue 617, design
// docs/plans/2026-11-10-collections-design.md §5 "Frontends"): a Bethesda
// game's plugin load order, the second axis beside mod order.
//
// It renders core.PluginOrderReport verbatim - the order, each plugin's
// flags, the masters it needs, what placed it - and never works an order
// out itself: every change is one core plugin edit (POST .../plugins, the
// same PlanPluginEdit/ApplyPluginEdit pair `lmm plugins` runs), and the
// tab re-renders the order the answer carries. So a rule or a master that
// holds a plugin in place shows as exactly that, not as a move that seemed
// to work. Unlike the Mods tab, nothing here is buffered for a Save: each
// change is written when it is made, which the tab says.
//
// Parity with the CLI: enable/disable (the row's checkbox), move (up/down,
// to the top or the end), sort (with the dry run's preview first), and the
// rules - add or remove a plugin's after/req/group, add or remove a group.

import { html, useState } from "../render.js";
import { editProfilePlugins } from "../api.js";

/** placementLabel is a row's "placed by" badge text. */
function placementLabel(entry) {
  switch (entry.placement) {
    case "implicit":
      return "game";
    case "external":
      return "external";
    default:
      return entry.placement;
  }
}

/** constraintText is a row's constraints in words, for the badge's title
 * and the row's detail line. */
function constraintText(entry) {
  return (entry.constraints ?? [])
    .map((c) => {
      switch (c.reason) {
        case "rule_after":
          return `rule: after ${c.from}`;
        case "rule_req":
          return `rule: requires ${c.from}`;
        case "group":
          return `rule: group ${c.group}`;
        default:
          return `master: ${c.from}`;
      }
    })
    .join(", ");
}

/** flagBadges is a row's M/L/D flags. */
function flagBadges(entry) {
  const out = [];
  if (entry.master) out.push(["M", "master"]);
  if (entry.light) out.push(["L", "light"]);
  if (entry.medium) out.push(["D", "medium"]);
  return out;
}

/** rowsOf is the rows the tab lists: the effective order for the active
 * profile, the recorded order for another (whose plugins are not
 * deployed). Each says whether the profile can change it. */
function rowsOf(report) {
  if (!report) return [];
  if (!report.active) {
    return (report.not_deployed ?? []).map((e, i) => ({
      position: i + 1,
      name: e.name,
      active: e.enabled,
      editable: true,
      entry: null,
    }));
  }
  return (report.plugins ?? []).map((e) => ({
    position: e.position,
    name: e.name,
    active: e.active,
    editable: e.managed,
    entry: e,
  }));
}

/**
 * PluginsTab renders the tab's panel for profileName. The report is the
 * modal's (it reads it to know whether the game has a plugin list at all,
 * and so whether to offer the tab), and every edit's answer goes back up
 * through onReport: the Mods tab unmounts this panel, so the order shown
 * on coming back must be the modal's, not state that left with it.
 */
export function PluginsTab({ profileName, context, report, onReport }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [notes, setNotes] = useState([]);
  const [sortPlan, setSortPlan] = useState(null);

  async function run(edit) {
    setBusy(true);
    setError(null);
    try {
      const res = await editProfilePlugins(profileName, edit, false, context);
      onReport(res.order);
      setNotes([
        ...(res.notes ?? []),
        ...(res.changes?.length ? [] : ["Nothing changed."]),
      ]);
      setSortPlan(null);
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  async function previewSort() {
    setBusy(true);
    setError(null);
    try {
      setSortPlan(
        await editProfilePlugins(profileName, { op: "sort" }, true, context),
      );
    } catch (err) {
      setError(err?.message ?? String(err));
    } finally {
      setBusy(false);
    }
  }

  const rows = rowsOf(report);
  const movable = rows.filter((r) => r.editable);
  function neighbour(row, delta) {
    const i = movable.findIndex((r) => r.name === row.name);
    return movable[i + delta]?.name;
  }

  return html`
    <div class="plugins-tab" data-testid="plugins-tab">
      <p class="plan__note">
        Plugins load top to bottom. Each change is saved as you make it
        ${report.active ? " and written to the game's Plugins.txt" : ""}.
        Plugins lmm did not deploy keep the place and state the game gave them.
      </p>
      ${
        report.list_file &&
        html`<p class="mono plugin-list__path">${report.list_file}</p>`
      }
      ${
        !report.active &&
        html`<p class="plan__note" data-testid="plugins-inactive">
          ${report.profile} is not the active profile: its plugins are not
          deployed, so this is its recorded order. It is written to Plugins.txt
          when the profile is deployed.
        </p>`
      }
      ${
        !report.sorted &&
        html`<p class="modal__error">
          The rules form a cycle, so this is the profile's order with no rule
          applied; nothing is written until a rule changes.
        </p>`
      }
      ${error && html`<p class="modal__error" data-testid="plugins-edit-error">${error}</p>`}
      ${notes.map((n) => html`<p class="plan__note">${n}</p>`)}

      <div class="plugins-tab__toolbar">
        <button
          type="button"
          class="button"
          data-action="plugins-sort"
          disabled=${busy || !report.active || !report.sorted}
          onClick=${previewSort}
        >
          Sort…
        </button>
      </div>
      ${
        sortPlan &&
        html`<section class="plan__section" data-testid="plugins-sort-plan">
          <h3 class="plan__heading">Sort</h3>
          ${
            sortPlan.no_changes
              ? html`<p class="plan__note">
                  The profile already records this order; sorting writes
                  Plugins.txt again.
                </p>`
              : html`<ul>
                  ${sortPlan.changes.map((c) => html`<li>${c}</li>`)}
                </ul>`
          }
          <div class="plugins-tab__toolbar">
            <button
              type="button"
              class="button"
              disabled=${busy}
              onClick=${() => setSortPlan(null)}
            >
              Cancel
            </button>
            <button
              type="button"
              class="button button--primary"
              data-action="plugins-sort-apply"
              disabled=${busy}
              onClick=${() => run({ op: "sort" })}
            >
              Apply sort
            </button>
          </div>
        </section>`
      }
      ${
        rows.length === 0
          ? html`<p class="plan__note">No plugins.</p>`
          : html`<ul
              class="reorder-list plugins-list"
              data-testid="plugins-list"
            >
              ${rows.map((row) => {
                const e = row.entry;
                const up = neighbour(row, -1);
                const down = neighbour(row, 1);
                const why = e ? constraintText(e) : "";
                return html`
                  <li
                    key=${row.name}
                    class="reorder-row plugins-row"
                    data-plugin=${row.name}
                    data-placement=${e?.placement ?? "order"}
                    data-active=${row.active ? "true" : "false"}
                  >
                    <span class="reorder-row__position mono"
                      >${row.position}</span
                    >
                    <input
                      type="checkbox"
                      data-action="plugin-toggle"
                      aria-label=${`${row.name} active`}
                      checked=${row.active}
                      disabled=${busy || !row.editable}
                      onChange=${(ev) =>
                        run({
                          op: ev.currentTarget.checked ? "enable" : "disable",
                          plugins: [row.name],
                        })}
                    />
                    <span class="reorder-row__name plugins-row__name">
                      <span class="mono">${row.name}</span>
                      ${flagBadges(e ?? {}).map(
                        ([f, title]) =>
                          html`<span class="badge badge--policy" title=${title}
                            >${f}</span
                          >`,
                      )}
                      ${
                        e &&
                        html`<span
                          class="badge badge--policy"
                          data-testid="plugin-placement"
                          title=${why || placementLabel(e)}
                          >${placementLabel(e)}</span
                        >`
                      }
                      ${
                        why &&
                        html`<span
                          class="plugins-row__detail"
                          data-testid="plugin-constraints"
                          >${why}</span
                        >`
                      }
                      ${
                        e?.masters?.length > 0 &&
                        html`<span class="plugins-row__detail">
                          needs
                          ${e.masters.map((m, i) => {
                            const missing = (e.missing_masters ?? []).includes(
                              m,
                            );
                            return html`${i > 0 ? ", " : " "}<span
                                class=${missing ? "plugin-list__finding--error" : ""}
                                data-missing=${missing ? "true" : null}
                                >${m}${missing ? " (missing)" : ""}</span
                              >`;
                          })}
                        </span>`
                      }
                      ${
                        e?.mod_name &&
                        html`<span class="plugins-row__detail"
                          >from ${e.mod_name}</span
                        >`
                      }
                    </span>
                    ${
                      row.editable &&
                      html`<span class="reorder-row__controls">
                        <button
                          type="button"
                          class="button button--small"
                          data-action="plugin-up"
                          aria-label=${`Move ${row.name} up`}
                          disabled=${busy || !up}
                          onClick=${() => run({ op: "move", plugin: row.name, before: up })}
                        >
                          ↑
                        </button>
                        <button
                          type="button"
                          class="button button--small"
                          data-action="plugin-down"
                          aria-label=${`Move ${row.name} down`}
                          disabled=${busy || !down}
                          onClick=${() => run({ op: "move", plugin: row.name, after: down })}
                        >
                          ↓
                        </button>
                        <button
                          type="button"
                          class="button button--small"
                          aria-label=${`Move ${row.name} to the top`}
                          disabled=${busy || !up}
                          onClick=${() => run({ op: "move", plugin: row.name, to: "first" })}
                        >
                          First
                        </button>
                        <button
                          type="button"
                          class="button button--small"
                          aria-label=${`Move ${row.name} to the end`}
                          disabled=${busy || !down}
                          onClick=${() => run({ op: "move", plugin: row.name, to: "last" })}
                        >
                          Last
                        </button>
                      </span>`
                    }
                  </li>
                `;
              })}
            </ul>`
      }
      ${
        report.active &&
        (report.not_deployed ?? []).length > 0 &&
        html`<p class="plan__note" data-testid="plugins-not-deployed">
          Kept in the profile's order, not deployed now:
          ${report.not_deployed.map((e) => e.name).join(", ")}
        </p>`
      }
      ${
        (report.findings ?? []).length > 0 &&
        html`<section class="plan__section" data-testid="plugins-findings">
          <h3 class="plan__heading">Findings</h3>
          <ul>
            ${report.findings.map(
              (f) =>
                html`<li
                  class=${f.severity === "error" ? "plugin-list__finding--error" : ""}
                  data-code=${f.code}
                >
                  ${f.severity === "error" ? "Error" : "Warning"}: ${f.message}
                </li>`,
            )}
          </ul>
        </section>`
      }

      <${PluginRules}
        report=${report}
        busy=${busy}
        names=${rows.filter((r) => r.editable).map((r) => r.name)}
        run=${run}
      />
    </div>
  `;
}

/** rulePartsOf is a plugin rule's parts - its group, each plugin it loads
 * after, each it requires - each with the rule_remove that takes it out,
 * as `lmm plugins rules remove --clear-group/--after/--req` does. */
function rulePartsOf(r) {
  const parts = [];
  if (r.group)
    parts.push({
      key: `group:${r.group}`,
      label: `in group ${r.group}`,
      rule: {},
      clearGroup: true,
    });
  for (const a of r.after ?? [])
    parts.push({
      key: `after:${a}`,
      label: `after ${a}`,
      rule: { after: [a] },
    });
  for (const q of r.req ?? [])
    parts.push({ key: `req:${q}`, label: `requires ${q}`, rule: { req: [q] } });
  return parts;
}

/** PluginRules is the tab's rules section: the profile's groups and
 * per-plugin rules, each removable, and the forms that add them. */
function PluginRules({ report, busy, names, run }) {
  const rules = report.rules ?? {};
  const groups = rules.groups ?? [];
  const plugins = rules.plugins ?? [];
  const [plugin, setPlugin] = useState("");
  const [kind, setKind] = useState("after");
  const [target, setTarget] = useState("");
  const [group, setGroup] = useState("");
  const [groupAfter, setGroupAfter] = useState("default");

  function addRule() {
    const rule = { name: plugin.trim() };
    if (kind === "group") rule.group = target.trim();
    else rule[kind] = [target.trim()];
    run({ op: "rule_add", rule });
    setTarget("");
  }

  function addGroup() {
    const after = groupAfter.trim();
    run({
      op: "group_add",
      group: { name: group.trim(), ...(after ? { after: [after] } : {}) },
    });
    setGroup("");
  }

  return html`
    <section class="plan__section" data-testid="plugins-rules">
      <h3 class="plan__heading">Rules</h3>
      ${
        groups.length === 0 && plugins.length === 0
          ? html`<p class="plan__note">No rules.</p>`
          : html`<ul class="plugins-rules__list">
              ${groups.map(
                (g) =>
                  html`<li key=${`g:${g.name}`} data-group=${g.name}>
                    <span
                      >Group <span class="mono">${g.name}</span>${
                        g.after?.length
                          ? ` loads after ${g.after.join(", ")}`
                          : ""
                      }</span
                    >
                    <button
                      type="button"
                      class="button button--small"
                      aria-label=${`Remove group ${g.name}`}
                      disabled=${busy}
                      onClick=${() => run({ op: "group_remove", group: { name: g.name } })}
                    >
                      Remove
                    </button>
                  </li>`,
              )}
              ${plugins.map(
                (r) =>
                  html`<li key=${`p:${r.name}`} data-rule=${r.name}>
                    <span class="plugins-rules__parts">
                      <span class="mono">${r.name}</span>
                      ${rulePartsOf(r).map(
                        (part) =>
                          html`<span
                            class="plugins-rules__part"
                            data-part=${part.key}
                          >
                            ${part.label}
                            <button
                              type="button"
                              class="button button--small"
                              aria-label=${`Remove "${part.label}" from the rule for ${r.name}`}
                              disabled=${busy}
                              onClick=${() =>
                                run({
                                  op: "rule_remove",
                                  rule: { name: r.name, ...part.rule },
                                  ...(part.clearGroup
                                    ? { clear_group: true }
                                    : {}),
                                })}
                            >
                              ×
                            </button>
                          </span>`,
                      )}
                    </span>
                    <button
                      type="button"
                      class="button button--small"
                      data-action="rule-remove"
                      aria-label=${`Remove the rule for ${r.name}`}
                      disabled=${busy}
                      onClick=${() =>
                        run({ op: "rule_remove", rule: { name: r.name } })}
                    >
                      Remove
                    </button>
                  </li>`,
              )}
            </ul>`
      }
      <datalist id="plugins-tab-names">
        ${names.map((n) => html`<option value=${n}></option>`)}
      </datalist>
      <form
        class="plugins-rules__form"
        data-testid="plugins-rule-form"
        onSubmit=${(e) => {
          e.preventDefault();
          addRule();
        }}
      >
        <input
          type="text"
          list="plugins-tab-names"
          name="rule-plugin"
          aria-label="Plugin the rule is about"
          placeholder="Plugin"
          value=${plugin}
          onInput=${(e) => setPlugin(e.currentTarget.value)}
        />
        <select
          name="rule-kind"
          aria-label="Rule kind"
          value=${kind}
          onChange=${(e) => setKind(e.currentTarget.value)}
        >
          <option value="after">loads after</option>
          <option value="req">requires</option>
          <option value="group">is in group</option>
        </select>
        <input
          type="text"
          list=${kind === "group" ? null : "plugins-tab-names"}
          name="rule-target"
          aria-label=${kind === "group" ? "Group" : "Other plugin"}
          placeholder=${kind === "group" ? "Group" : "Plugin"}
          value=${target}
          onInput=${(e) => setTarget(e.currentTarget.value)}
        />
        <button
          type="submit"
          class="button"
          data-action="rule-add"
          disabled=${busy || !plugin.trim() || !target.trim()}
        >
          Add rule
        </button>
      </form>
      <form
        class="plugins-rules__form"
        data-testid="plugins-group-form"
        onSubmit=${(e) => {
          e.preventDefault();
          addGroup();
        }}
      >
        <input
          type="text"
          name="group-name"
          aria-label="New group name"
          placeholder="Group"
          value=${group}
          onInput=${(e) => setGroup(e.currentTarget.value)}
        />
        <input
          type="text"
          name="group-after"
          aria-label="Group it loads after"
          placeholder="Loads after group"
          value=${groupAfter}
          onInput=${(e) => setGroupAfter(e.currentTarget.value)}
        />
        <button
          type="submit"
          class="button"
          data-action="group-add"
          disabled=${busy || !group.trim()}
        >
          Add group
        </button>
      </form>
    </section>
  `;
}
