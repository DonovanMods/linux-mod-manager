// fomod.js - the FOMOD installer wizard's pure half (issue 615): reading a
// question document (core.FomodChoicesRequiredError's details), checking a
// selection against its group's rule, and turning selections into the
// answers a re-posted job carries in its "fomod" option.
//
// The walk rule is the CLI's (cmd/lmm/fomod.go): a question lists only the
// steps visible under its preselection, so the wizard walks a mod step by
// step while the user keeps each step's preselection, and the first step
// the user changes ends that mod's round - its answers so far go back to
// the server, which asks again about whatever they made visible. No
// rendering and no fetching here, so the module stays testable on its own.

/**
 * fomodQuestionFor returns the FOMOD question an error envelope's details
 * (or a result's fomod_choices_required) carry, or null. Identified by its
 * shape - a mods list whose entries carry steps and a config hash - never
 * by the message text.
 */
export function fomodQuestionFor(details) {
  const mods = details?.mods;
  if (!Array.isArray(mods) || mods.length === 0) return null;
  const ok = mods.every(
    (m) =>
      m &&
      typeof m.config_sha256 === "string" &&
      Array.isArray(m.steps) &&
      typeof m.source_id === "string",
  );
  return ok ? details : null;
}

/** modKey is the answer map's key for a question's mod. */
export function modKey(mod) {
  return `${mod.source_id}:${mod.mod_id}`;
}

/** mergeQuestions folds several questions (an update batch's skips) into
 * one document, in order. Null when there are none. */
export function mergeQuestions(questions) {
  const mods = questions.flatMap((q) => q?.mods ?? []);
  return mods.length > 0 ? { mods } : null;
}

/** groupIsFree reports a group the user can change: not SelectAll, with an
 * option that is neither Required nor NotUsable. */
export function groupIsFree(group) {
  if (group.type === "SelectAll") return false;
  return group.options.some(
    (o) => o.type !== "Required" && o.type !== "NotUsable",
  );
}

/** stepIsFree reports a step with a group to choose in. */
export function stepIsFree(step) {
  return step.groups.some(groupIsFree);
}

/** groupRule words a group's rule. */
export function groupRule(type) {
  switch (type) {
    case "SelectExactlyOne":
      return "Choose one";
    case "SelectAtMostOne":
      return "Choose one, or none";
    case "SelectAtLeastOne":
      return "Choose at least one";
    case "SelectAll":
      return "All of these are installed";
    default:
      return "Choose any";
  }
}

/** isSingle reports a group answered with radio buttons. */
export function isSingle(group) {
  return group.type === "SelectExactlyOne" || group.type === "SelectAtMostOne";
}

/** preselection is a mod's question as selections: [step][group][option]. */
export function preselection(mod) {
  return mod.steps.map((s) =>
    s.groups.map((g) => g.options.map((o) => Boolean(o.selected))),
  );
}

/**
 * selectionProblem checks one group's selection against its rule and its
 * options' types; null when it is valid. Required options stay selected and
 * NotUsable ones unselected (unless the question already had it selected:
 * a recorded pick of an unusable option is honoured, as core does).
 */
export function selectionProblem(group, sel) {
  let picked = 0;
  let usable = 0;
  for (const [i, o] of group.options.entries()) {
    if (o.type === "Required" && !sel[i]) return `"${o.name}" is required.`;
    if (o.type === "NotUsable" && sel[i] && !o.selected)
      return `"${o.name}" cannot be used here.`;
    if (o.type !== "NotUsable") usable += 1;
    if (sel[i]) picked += 1;
  }
  switch (group.type) {
    case "SelectExactlyOne":
      return picked !== 1 && usable > 0 ? "Choose exactly one." : null;
    case "SelectAtMostOne":
      return picked > 1 ? "Choose at most one." : null;
    case "SelectAtLeastOne":
      return picked === 0 && usable > 0 ? "Choose at least one." : null;
    default:
      return null;
  }
}

/** choose returns a group's selection after the user picks option i. */
export function choose(group, sel, i, checked) {
  const next = isSingle(group) ? sel.map(() => false) : [...sel];
  next[i] = checked;
  // Required options never leave the selection.
  group.options.forEach((o, j) => {
    if (o.type === "Required") next[j] = true;
  });
  return next;
}

function sameSelection(a, b) {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

/** stepChanged reports a step whose selections differ from the question's
 * preselection. */
export function stepChanged(mod, stepIndex, stepSel) {
  return mod.steps[stepIndex].groups.some(
    (g, gi) =>
      !sameSelection(
        g.options.map((o) => Boolean(o.selected)),
        stepSel[gi],
      ),
  );
}

function repeated(names, name) {
  return names.filter((n) => n === name).length > 1;
}

/** optionChoices is one group's selection in canonical form: the option's
 * position as idx, and its ordinal as index only where its name repeats. */
function optionChoices(group, sel) {
  const names = group.options.map((o) => o.name);
  const out = [];
  group.options.forEach((o, i) => {
    if (!sel[i]) return;
    const c = { name: o.name, idx: i };
    if (repeated(names, o.name)) c.index = o.ordinal;
    out.push(c);
  });
  return out;
}

/**
 * answerFor builds a mod's answer from its selections, covering its first
 * `through` steps (a round's prefix), in the profile's fomod: shape. Each
 * step's own index is taken from the question's canonical choices, which
 * line up with its steps.
 */
export function answerFor(mod, selections, through) {
  const steps = mod.steps.slice(0, through).map((step, si) => {
    const canonical = mod.choices?.[si];
    const out = { name: step.name, groups: [] };
    if (canonical?.name === step.name && canonical.index !== undefined)
      out.index = canonical.index;
    const groupNames = step.groups.map((g) => g.name);
    step.groups.forEach((g, gi) => {
      const gc = {
        name: g.name,
        choices: optionChoices(g, selections[si][gi]),
      };
      const cg = canonical?.groups?.[gi];
      if (cg?.name === g.name && cg.index !== undefined) gc.index = cg.index;
      else if (repeated(groupNames, g.name)) gc.index = g.ordinal;
      out.groups.push(gc);
    });
    return out;
  });
  return { config_sha256: mod.config_sha256, steps };
}

/**
 * wizardPages lists the pages a round shows: for each mod, every step from
 * where the walk stopped last round (start[key]) that has something to
 * choose. A mod whose walk already reached its end starts over: the server
 * asked again about everything it had.
 */
export function wizardPages(question, start = {}) {
  const pages = [];
  question.mods.forEach((mod, mi) => {
    let from = start[modKey(mod)] ?? 0;
    if (from >= mod.steps.length) from = 0;
    mod.steps.forEach((step, si) => {
      if (si >= from && stepIsFree(step)) pages.push({ mod: mi, step: si });
    });
  });
  return pages;
}

/** imageURL is the read-only route serving a question's option image. */
export function imageURL(mod, image) {
  const q = new URLSearchParams({
    source: mod.source_id,
    mod: mod.mod_id,
    config: mod.config_sha256,
    path: image,
  });
  return `/api/v1/fomod/images?${q.toString()}`;
}
