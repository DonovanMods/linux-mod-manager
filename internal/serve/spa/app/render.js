// render.js - the rendering primitives, bound once.
//
// Every component imports h/html/render from here rather than from the
// vendored files directly, so the vendored paths appear in exactly one
// place and htm is bound to Preact's h exactly once.
//
// SAFETY: all DOM in this application goes through Preact, which escapes
// text by construction. dangerouslySetInnerHTML - and every other raw-HTML
// write - is forbidden package-wide and ratcheted by
// no_unsafe_dom_test.go. There is no escape hatch and there is no need for
// one: nothing this UI renders is trusted markup.

export { h, render, Fragment } from "/vendor/preact.module.js";
export {
  useState,
  useEffect,
  useRef,
  useMemo,
  useCallback,
} from "/vendor/hooks.module.js";

import { h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";

/** htm bound to Preact's h: JSX-like templates with no build step. */
export const html = htm.bind(h);
