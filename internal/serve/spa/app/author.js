// author.js - the one place this SPA turns a mod's author wire fields into
// text (issue 420), the twin of core.AuthorText.
//
// A Steam Workshop item's `author` is its creator's steamid64 - an id, not
// a name. Core (the source, and the installed-mod listing from the
// source's cache) adds `author_name` beside it when the id resolves to a
// persona name. Every surface shows the name when there is one and keeps
// the id one hover away, in the element's title.

/** displayAuthor is the author text for mod: author_name, else author,
 * else "". */
export function displayAuthor(mod) {
  if (!mod) return "";
  return mod.author_name || mod.author || "";
}

/** authorTitle is the title for an author element: the id a resolved
 * name stands for, or undefined (no title attribute) otherwise. */
export function authorTitle(mod) {
  if (!mod?.author_name || !mod.author) return undefined;
  return mod.author;
}
