// theme.js - the light/dark override the top bar's theme control drives.
//
// Three states, not two (docs/plans/2026-08-31-serve-spa-design.md §Visual
// design): "system" follows prefers-color-scheme and persists nothing,
// while "light" and "dark" are explicit overrides stamped on <html> as
// data-theme and remembered in localStorage.
//
// The stamping itself is duplicated - once here and once in the shell's
// pre-paint inline script - and that is the point: the inline copy runs
// before the first paint so there is no flash, this copy owns every change
// after it.

const STORAGE_KEY = "lmm-theme";

/** The three values setTheme accepts, in the order a toggle cycles them. */
export const THEMES = ["system", "light", "dark"];

/**
 * The persisted override, or "system" when there is none - which is also
 * the answer when storage cannot be read at all (a private window, storage
 * disabled). An unreadable preference is not an error; it just means the
 * system preference is what applies.
 */
export function currentTheme() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return stored === "light" || stored === "dark" ? stored : "system";
  } catch {
    return "system";
  }
}

/**
 * Applies theme and persists it. "system" removes the override rather than
 * recording a third value, so a machine that later switches to dark follows
 * it. Returns the theme actually applied.
 */
export function setTheme(theme) {
  const next = THEMES.includes(theme) ? theme : "system";
  const root = document.documentElement;
  if (next === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", next);
  }
  try {
    if (next === "system") {
      localStorage.removeItem(STORAGE_KEY);
    } else {
      localStorage.setItem(STORAGE_KEY, next);
    }
  } catch {
    // Nothing to persist to. The theme still applies for this page view.
  }
  return next;
}

/** Cycles system -> light -> dark -> system, applying the next value. */
export function cycleTheme() {
  const at = THEMES.indexOf(currentTheme());
  return setTheme(THEMES[(at + 1) % THEMES.length]);
}

/**
 * Reports whether the page is currently rendering dark, override or system.
 * For the few places that need to know the RESOLVED appearance rather than
 * the preference (an icon, say).
 */
export function resolvedTheme() {
  const override = currentTheme();
  if (override !== "system") return override;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}
