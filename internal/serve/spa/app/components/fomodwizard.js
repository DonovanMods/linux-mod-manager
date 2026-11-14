// fomodwizard.js - the FOMOD installer wizard (issue 615, design
// docs/plans/2026-11-10-collections-design.md §4 "Plan/Apply without
// callbacks").
//
// A job whose mod turned out to be a FOMOD installer with choices lmm has
// no answers for fails with the installer's question (or, for a batch,
// reports it on its result) and writes nothing. This modal asks it, one
// step per page, and hands the answers to main.js, which re-posts the same
// plan and job with them in "fomod" - Apply never calls back into the
// page. The walk rule is fomod.js's: the first step the user changes ends
// that mod's round, because it may show or hide later steps, and the
// server's next question picks up where this one stopped.

import { html, useState } from "../render.js";
import { Modal } from "./modal.js";
import {
  answerFor,
  answerKey,
  choose,
  groupIsFree,
  groupRule,
  imageURL,
  isSingle,
  modKey,
  preselection,
  questionTitle,
  selectionProblem,
  stepChanged,
  wizardPages,
} from "../fomod.js";

/** typeNote is the short label an option's resolved type earns. */
function typeNote(type) {
  switch (type) {
    case "Required":
      return "required";
    case "Recommended":
      return "recommended";
    case "NotUsable":
      return "not usable";
    case "CouldBeUsable":
      return "may not work";
    default:
      return "";
  }
}

/** reasonNote introduces a mod's question when it is not a first install. */
function reasonNote(mod) {
  if (mod.reason === "stale")
    return "The choices recorded for this mod no longer fit its installer. Review them.";
  if (mod.reason === "reconfigure")
    return "Your current choices are selected. Change what you want installed.";
  if (mod.reason === "archive_mismatch") {
    // Issue 609: a collection's choices replay only against the archive they
    // were recorded against.
    const sums = mod.archive_mismatch
      ? ` (The collection records md5 ${mod.archive_mismatch.expected_md5}; this archive's is ${mod.archive_mismatch.actual_md5}.)`
      : "";
    return `This is not the archive the collection's choices were recorded against, so they were not replayed. The collection's choices are selected: review them.${sums}`;
  }
  return "";
}

/**
 * FomodWizardModal is the wizard over modal.question (the question
 * document), starting each mod where the previous round stopped
 * (modal.start, by mod key). Finishing calls actions.submitFomod with the
 * answers and where each mod's walk stopped.
 */
export function FomodWizardModal({ modal, actions }) {
  const question = modal?.question;
  const [selections, setSelections] = useState(() =>
    (question?.mods ?? []).map(preselection),
  );
  const [history, setHistory] = useState([0]);
  const [ends, setEnds] = useState({});
  const [problem, setProblem] = useState("");
  // defaulted are the mods answered with the installer's own defaults
  // (--fomod-defaults' counterpart): their remaining steps are skipped.
  const [defaulted, setDefaulted] = useState({});
  if (modal?.type !== "fomod" || !question) return null;

  const pages = wizardPages(question, modal.start ?? {});
  const pageIndex = history[history.length - 1];
  const page = pages[pageIndex];

  function nextPageAfter(index, mi, changed) {
    if (!changed) return index + 1 < pages.length ? index + 1 : -1;
    const later = pages.findIndex((p, i) => i > index && p.mod > mi);
    return later;
  }

  function submit(finalEnds, finalDefaulted = defaulted) {
    const answers = {};
    const walked = {};
    // Answers are keyed per question: the mod's, or one file's installer
    // of a mod with several (issue 634).
    question.mods.forEach((mod, mi) => {
      const key = answerKey(mod);
      if (finalDefaulted[mi]) {
        // The installer's own preselection: the question's defaults when it
        // carries them (a recording or earlier answers chose otherwise),
        // else its choices, which then ARE the defaults.
        const steps = mod.defaults?.length ? mod.defaults : mod.choices;
        answers[key] = { config_sha256: mod.config_sha256, steps };
        walked[key] = mod.steps.length;
        return;
      }
      const through = finalEnds[mi] ?? mod.steps.length;
      answers[key] = answerFor(mod, selections[mi], through);
      walked[key] = through;
    });
    const mods = [...new Set(question.mods.map(modKey))];
    actions.submitFomod(modal.origin, answers, walked, mods);
  }

  if (!page) {
    // Nothing to choose this round (every remaining step is fixed): the
    // preselection is the answer.
    return html`
      <${Modal}
        kind="fomod"
        title=${modal.title || "Installer options"}
        openerSelector=${modal.openerSelector}
        onClose=${actions.closeModal}
        footer=${html`
          <button type="button" class="button" onClick=${actions.closeModal}>
            Cancel
          </button>
          <button
            type="button"
            class="button button--primary"
            data-action="fomod-finish"
            onClick=${() => submit(ends)}
          >
            Finish
          </button>
        `}
      >
        <p class="plan__summary">
          The installer has nothing left to choose. Finish installs it.
        </p>
      <//>
    `;
  }

  const mod = question.mods[page.mod];
  const step = mod.steps[page.step];
  const stepSel = selections[page.mod][page.step];
  const changed = stepChanged(mod, page.step, stepSel);
  const next = nextPageAfter(pageIndex, page.mod, changed);
  const finishing = next < 0;
  const firstOfMod = pages.findIndex((p) => p.mod === page.mod) === pageIndex;

  function setGroup(gi, sel) {
    setProblem("");
    setSelections((all) =>
      all.map((m, mi) =>
        mi !== page.mod
          ? m
          : m.map((s, si) =>
              si !== page.step ? s : s.map((g, j) => (j === gi ? sel : g)),
            ),
      ),
    );
  }

  function goNext() {
    for (const [gi, g] of step.groups.entries()) {
      if (!groupIsFree(g)) continue;
      const why = selectionProblem(g, stepSel[gi]);
      if (why) {
        setProblem(`${g.name || "This group"}: ${why}`);
        return;
      }
    }
    const nextEnds = { ...ends };
    if (changed) nextEnds[page.mod] = page.step + 1;
    else delete nextEnds[page.mod];
    setEnds(nextEnds);
    if (next < 0) {
      submit(nextEnds);
      return;
    }
    setHistory([...history, next]);
  }

  function goBack() {
    if (history.length < 2) return;
    const prev = history[history.length - 2];
    const prevPage = pages[prev];
    setEnds((e) => {
      const copy = { ...e };
      if ((copy[prevPage.mod] ?? Infinity) > prevPage.step)
        delete copy[prevPage.mod];
      return copy;
    });
    setDefaulted((d) => {
      const copy = { ...d };
      delete copy[prevPage.mod];
      return copy;
    });
    setProblem("");
    setHistory(history.slice(0, -1));
  }

  const visibleSteps = mod.steps.length;
  const note = firstOfMod ? reasonNote(mod) : "";

  /** useDefaults answers this page's mod with the installer's defaults and
   * moves on to the next mod that asks, or finishes. */
  function useDefaults() {
    const nextDefaulted = { ...defaulted, [page.mod]: true };
    setDefaulted(nextDefaulted);
    setProblem("");
    const later = pages.findIndex((p, i) => i > pageIndex && p.mod > page.mod);
    if (later < 0) {
      submit(ends, nextDefaulted);
      return;
    }
    setHistory([...history, later]);
  }

  return html`
    <${Modal}
      kind="fomod"
      title=${modal.title || "Installer options"}
      openerSelector=${modal.openerSelector}
      onClose=${actions.closeModal}
      footer=${html`
        <button
          type="button"
          class="button"
          data-action="fomod-cancel"
          onClick=${actions.closeModal}
        >
          Cancel
        </button>
        <button
          type="button"
          class="button"
          data-action="fomod-back"
          disabled=${history.length < 2}
          onClick=${goBack}
        >
          Back
        </button>
        <button
          type="button"
          class="button button--primary"
          data-action=${finishing ? "fomod-finish" : "fomod-next"}
          onClick=${goNext}
        >
          ${finishing ? "Finish" : "Next"}
        </button>
      `}
    >
      <div
        class="fomod"
        data-mod=${modKey(mod)}
        data-file=${mod.file_id || undefined}
        data-step=${step.name}
        data-testid="fomod-step"
      >
        <p class="fomod__mod">
          ${questionTitle(mod)}${mod.module_name ? ` - ${mod.module_name}` : ""}
        </p>
        ${
          mod.file_id &&
          html`<p class="plan__option-hint" data-testid="fomod-file">
            This mod has an installer in each of its files; this one is in
            ${" "}<span class="mono">${mod.file_name || mod.file_id}</span>.
          </p>`
        }
        ${note && html`<p class="plan__note plan__note--warn">${note}</p>`}
        <p class="fomod__shortcut">
          <button
            type="button"
            class="button button--small"
            data-action="fomod-defaults"
            onClick=${useDefaults}
          >
            Use the installer's defaults
          </button>
          ${" "}<span class="plan__option-hint"
            >Answers
            ${question.mods.length > 1 ? (mod.file_id ? "this installer" : "this mod") : "every step"}
            as the installer suggests, like --fomod-defaults.</span
          >
        </p>
        <h3 class="fomod__step">
          Step ${page.step + 1} of ${visibleSteps}: ${step.name || "(unnamed)"}
        </h3>
        ${step.groups.map(
          (g, gi) =>
            html`<${FomodGroup}
              key=${`${gi}:${g.name}`}
              mod=${mod}
              group=${g}
              name=${`fomod-${page.mod}-${page.step}-${gi}`}
              sel=${stepSel[gi]}
              onChange=${(sel) => setGroup(gi, sel)}
            />`,
        )}
        ${
          problem &&
          html`<p class="modal__error" role="alert" data-testid="fomod-problem">
            ${problem}
          </p>`
        }
        ${
          changed &&
          !finishing &&
          html`<p class="plan__option-hint">
            Your change may show or hide later steps; lmm checks the installer
            again before going on.
          </p>`
        }
      </div>
    <//>
  `;
}

/** FomodGroup renders one group: radios for a one-pick rule, checkboxes
 * otherwise, each option with its type, description and image. */
function FomodGroup({ mod, group, name, sel, onChange }) {
  const free = groupIsFree(group);
  const single = isSingle(group);
  return html`
    <fieldset class="fomod__group" data-group=${group.name}>
      <legend class="fomod__legend">
        ${group.name || "Options"}
        <span class="fomod__rule">${groupRule(group.type)}</span>
      </legend>
      ${group.options.map((o, i) => {
        const locked =
          !free ||
          o.type === "Required" ||
          (o.type === "NotUsable" && !o.selected);
        const note = typeNote(o.type);
        return html`
          <label
            class="fomod__option"
            key=${`${i}:${o.name}`}
            data-option=${o.name}
          >
            <input
              type=${single ? "radio" : "checkbox"}
              name=${name}
              checked=${Boolean(sel[i])}
              disabled=${locked}
              onChange=${(e) => onChange(choose(group, sel, i, e.currentTarget.checked))}
            />
            <span class="fomod__option-text">
              <span class="fomod__option-name">${o.name || "(unnamed)"}</span>
              ${note && html` <span class="fomod__option-type">(${note})</span>`}
              ${
                o.description &&
                html`<span class="fomod__description">${o.description}</span>`
              }
            </span>
            ${
              o.image &&
              html`<img
                class="fomod__image"
                src=${imageURL(mod, o.image)}
                alt=""
                loading="lazy"
                data-testid="fomod-image"
              />`
            }
          </label>
        `;
      })}
      ${
        group.type === "SelectAtMostOne" &&
        free &&
        html`
          <label class="fomod__option" data-option="">
            <input
              type="radio"
              name=${name}
              checked=${!sel.some(Boolean)}
              onChange=${() => onChange(sel.map(() => false))}
            />
            <span class="fomod__option-text">
              <span class="fomod__option-name">None</span>
            </span>
          </label>
        `
      }
    </fieldset>
  `;
}
