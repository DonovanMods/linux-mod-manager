// unchanged.js - the sentence for mods a check answered from what lmm
// recorded the last time it checked them (issue 672): their source said
// they had not changed, so their file lists were not fetched again. The
// same words as core.UnchangedSummary, which the CLI prints.

/** unchangedSummary is that sentence for n mods, or "" for none. */
export function unchangedSummary(n) {
  if (!(n > 0)) return "";
  return `${n} ${n === 1 ? "mod" : "mods"} unchanged since the last check, not asked again`;
}
