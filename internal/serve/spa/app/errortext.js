// errortext.js - renders a core error message as the web, not as a
// terminal.
//
// core writes one error string for both frontends, and it is written for a
// terminal: `authentication required: Steam Web API key required (run `lmm
// auth login steamworkshop`)`. In a terminal those backticks are the
// conventional way to set a command apart. In a browser they are literal
// punctuation - the user sees the grave accents and has to work out that
// they are not part of the command (issue 402).
//
// So the web reads them the way the message means them, and sets what they
// enclose in the monospace face the rest of this UI uses for commands and
// identifiers. Nothing is parsed beyond that pairing: this is NOT markdown,
// and never becomes markup - the pieces are plain text nodes and <code>
// elements built by htm, which is why the no-unsafe-DOM ratchet has nothing
// to catch here.

import { html } from "./render.js";

/** codeSpans splits text on PAIRED backticks and returns an array of plain
 * strings and <code> vnodes, ready to interpolate into a template.
 *
 * An UNPAIRED backtick is left exactly as it was typed. That is the whole
 * safety rule of this function: a message it does not understand comes
 * through unchanged rather than half-transformed, so the worst case is
 * today's behaviour rather than a mangled sentence.
 *
 * A non-string (null, undefined, an Error) is returned untouched, so a
 * caller can hand it whatever its error slot holds. */
export function codeSpans(text) {
  if (typeof text !== "string" || !text.includes("`")) return text;

  const parts = [];
  let rest = text;
  for (;;) {
    const open = rest.indexOf("`");
    if (open === -1) break;
    const close = rest.indexOf("`", open + 1);
    if (close === -1) break;
    if (open > 0) parts.push(rest.slice(0, open));
    parts.push(html`<code class="mono">${rest.slice(open + 1, close)}</code>`);
    rest = rest.slice(close + 1);
  }
  if (rest) parts.push(rest);
  return parts;
}
