// deploypath.js - how the SPA spells a deployed file (issue 629). A file
// lmm deploys into the game folder itself, beside its executable (a script
// extender's loader, an ENB's d3d11.dll), rather than into the mod folder,
// arrives from core as its path relative to that folder plus root:
// "install_path". The SPA shows it with the root named in front, as the CLI
// does - "<game root>/d3d11.dll" - so it never reads as the mod-folder file
// of the same name. A file lmm deploys into the game's Wine/Proton prefix
// (issue 636: Baldur's Gate 3's .pak mods) arrives with root "prefix", its
// path relative to the prefix's user folder, and is shown as
// "<prefix>/AppData/...". A path with no root is a mod-folder file, shown
// as is.

/** GAME_ROOT is core's label for the game folder (domain.GameRootLabel). */
export const GAME_ROOT = "<game root>";

/** PREFIX_ROOT is core's label for the prefix's user folder
 * (domain.PrefixLabel). */
export const PREFIX_ROOT = "<prefix>";

/** isGameRoot reports whether a document's root field names the game
 * folder. */
export function isGameRoot(root) {
  return root === "install_path";
}

/** isPrefixRoot reports whether a document's root field names the
 * prefix's user folder (issue 636). */
export function isPrefixRoot(root) {
  return root === "prefix";
}

/** deployedPath is path, relative to root, as a person reads it. */
export function deployedPath(path, root) {
  const p = typeof path === "string" ? path : "";
  if (isGameRoot(root)) return `${GAME_ROOT}/${p}`;
  if (isPrefixRoot(root)) return `${PREFIX_ROOT}/${p}`;
  return p;
}

/** deployedKey is a list key for path under root: unique per file, so a
 * mod-folder folder literally named "<game root>" never shares a key with
 * the game-root file its display spelling matches. */
export function deployedKey(path, root) {
  const r = isGameRoot(root)
    ? "install_path"
    : isPrefixRoot(root)
      ? "prefix"
      : "mod_path";
  return `${r}:${typeof path === "string" ? path : ""}`;
}
