// shortcuts.js - the keyboard bindings this application has, as data.
//
// The design's own modal inventory (docs/plans/2026-08-31-serve-spa-design.md
// §Modals) lists a "keyboard-shortcuts help" modal, and the README carries a
// Keyboard table documenting every binding. Two hand-maintained copies of
// the same list is two lists that drift, so there is ONE: this module is the
// source, components/shortcutsmodal.js renders it, and a Go test
// (internal/serve/shortcuts_readme_test.go) fails the build if the README's
// table stops matching it row for row.
//
// Plain text, not markup: the README wraps the keys in backticks and the
// modal renders them in <kbd>, and each surface is free to decorate what it
// is given. Nothing here knows about either.

/** shortcutRows is every binding, in the order both surfaces present it:
 * the ones that work anywhere first, then the ones scoped to one surface.
 *
 * Each row is {keys, where, what} - the README table's three columns, in
 * its column order, so the two are comparable without a mapping. */
export const shortcutRows = [
  {
    keys: "Tab / Shift+Tab",
    where: "anywhere",
    what: "move through the controls; the first stop on every screen is Skip to content, which jumps past the top bar",
  },
  {
    keys: "?",
    where: "anywhere outside a text field",
    what: "open this keyboard-shortcuts help",
  },
  {
    keys: "Enter",
    where: "omnibar",
    what: "search the game's sources for what you typed",
  },
  {
    keys: "Esc",
    where: "any modal, the slide-over, any open dropdown",
    what: "close it and return focus to whatever opened it",
  },
  {
    keys: "← / →",
    where: "the slide-over",
    what: "step to the previous/next mod in the library's current order",
  },
  {
    keys: "← / → / Home / End",
    where: "the Setup page's section tabs",
    what: "move between sections",
  },
];
