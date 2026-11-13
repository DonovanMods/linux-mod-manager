// deploypath.js - how the SPA spells a deployed file (issue 629). A file
// lmm deploys into the game folder itself, beside its executable (a script
// extender's loader, an ENB's d3d11.dll), rather than into the mod folder,
// arrives from core as its path relative to that folder plus root:
// "install_path". The SPA shows it with the root named in front, as the CLI
// does - "<game root>/d3d11.dll" - so it never reads as the mod-folder file
// of the same name. A path with no root is a mod-folder file, shown as is.

/** GAME_ROOT is core's label for the game folder (domain.GameRootLabel). */
export const GAME_ROOT = "<game root>";

/** isGameRoot reports whether a document's root field names the game
 * folder. */
export function isGameRoot(root) {
  return root === "install_path";
}

/** deployedPath is path, relative to root, as a person reads it. */
export function deployedPath(path, root) {
  const p = typeof path === "string" ? path : "";
  return isGameRoot(root) ? `${GAME_ROOT}/${p}` : p;
}
