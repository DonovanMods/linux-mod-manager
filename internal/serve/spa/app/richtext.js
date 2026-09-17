// richtext.js - a mod description's BBCode or Markdown as a SAFE element
// tree (issue 419).
//
// The full mod page renders core.ModDetail.description_text: the source's
// description with its HTML already stripped (issue 342). What survives that
// cleaner is the markup that is not angle brackets - NexusMods' BBCode
// ("[b]…[/b]", "[url=…]") and the Markdown CurseForge, Thunderstore and
// Workshop authors write - and until now the reader saw it as literal
// brackets and asterisks.
//
// SAFETY, by construction rather than by sanitising:
//   - parseRichText returns plain data (see the node shapes below), never an
//     HTML string, and renderRichText maps each node kind to exactly one
//     allow-listed element through Preact's h, which escapes every text node.
//     There is no innerHTML anywhere (no_unsafe_dom_test.go).
//   - A link's target must parse as an absolute http(s) URL; anything else
//     (javascript:, data:, a relative path) falls back to the literal source
//     text. Every anchor opens in a new tab with rel="noopener noreferrer"
//     and says so, visibly and to assistive technology.
//   - An image is never an <img>: it becomes a link to the image, so a
//     description cannot make the browser fetch anything on its own.
//   - No attribute a description supplies reaches the DOM except a vetted
//     href: [size], [color], [font] and alignment tags are dropped, keeping
//     their content.
//   - Markup this file does not recognise - an unknown tag, an unclosed or
//     stray one - stays literal text.
//
// ROBUSTNESS: nesting is capped at MAX_DEPTH in both parsers - a further
// opening tag or quote marker is literal text - so no walk over the tree,
// and no render of it, recurses without bound; and parseRichText falls back
// to plain paragraphs if anything below it throws anyway.
//
// Node shapes (what the table tests in e2e_richtext_test.go compare):
//   string                              text
//   { t: "p" | "blockquote" | "ul" | "ol" | "li" | "strong" | "em" | "u" |
//        "s" | "code", c: [nodes] }
//   { t: "pre", text }                  a code block, verbatim
//   { t: "h", level: 1-3, c }           a heading (rendered h3-h5: the page
//                                       already owns h1 and h2)
//   { t: "a", href, c }                 an external link
//   { t: "img", href, alt }             an image, rendered as a link
//   { t: "br" } | { t: "hr" }

import { h } from "./render.js";

/**
 * safeHref returns href as a normalised absolute http(s) URL, or "" when it
 * is anything else - the one gate every link and image passes.
 */
export function safeHref(href) {
  const raw = String(href ?? "").trim();
  if (!/^https?:\/\//i.test(raw)) return "";
  try {
    const url = new URL(raw);
    if (url.protocol !== "http:" && url.protocol !== "https:") return "";
    return url.href;
  } catch {
    return "";
  }
}

// A BBCode tag this file knows. Anything bracketed that is not one of these
// is ordinary text ("[WIP]", "[1.2]").
const BB_TAG =
  /\[(\/?)(b|i|u|s|strike|url|img|list|\*|quote|code|size|color|colour|font|center|left|right|justify|spoiler|heading|line|youtube|h1|h2|h3)(?:=([^\]\n]*))?\]/gi;

const BB_DETECT = new RegExp(BB_TAG.source, "i");

// MAX_DEPTH is how deeply markup may nest: open BBCode tags, or Markdown
// quote levels. Past it, a further opener is text (issue 419: 33,000
// nested [b] tags overflowed the stack).
const MAX_DEPTH = 32;

// Markdown signals, one per construct: a line that opens a heading, a list
// item, a quote or a fence; or an inline link, strong run or code span.
const MD_DETECT =
  /(^|\n)[ \t]*(#{1,6}[ \t]+\S|[-*+][ \t]+\S|\d+[.)][ \t]+\S|>[ \t]?\S|```|(-{3,}|\*{3,}|_{3,})[ \t]*(\n|$))|!?\[[^\]\n]+\]\([^)\s]+\)|\*\*\S[^\n]*?\*\*|__\S[^\n]*?__|`[^`\n]+`/;

/**
 * detectFamily names the markup a description is written in: "bbcode",
 * "markdown" or "plain". BBCode wins a tie - a NexusMods description's
 * "[*]" item would otherwise read as nothing in particular.
 */
export function detectFamily(text) {
  const s = String(text ?? "");
  if (BB_DETECT.test(s)) return "bbcode";
  if (MD_DETECT.test(s)) return "markdown";
  return "plain";
}

/**
 * parseRichText turns a description into { family, nodes }. It never
 * throws on what a description contains, and an empty description yields
 * no nodes.
 */
export function parseRichText(text) {
  const s = String(text ?? "").replace(/\r\n?/g, "\n");
  try {
    const family = detectFamily(s);
    if (!s.trim()) return { family, nodes: [] };
    if (family === "bbcode") return { family, nodes: parseBBCode(s) };
    if (family === "markdown") return { family, nodes: parseMarkdown(s, 0) };
    return { family, nodes: plainParagraphs(s) };
  } catch {
    // The parsers are written not to throw; this makes it a guarantee.
    return { family: "plain", nodes: plainParagraphs(s) };
  }
}

// plainParagraphs is the page's long-standing rendering: blank-line
// separated runs, each one a paragraph whose single newlines are kept. The
// cleaner turns </p><p> into two newlines and <br> into one, so a source
// that uses both leaves runs of three - one paragraph break, not a gap.
function plainParagraphs(s) {
  return s
    .split(/\n\s*\n/)
    .map((para) => para.trim())
    .filter(Boolean)
    .map((para) => ({ t: "p", c: withBreaks([para]) }));
}

// ---------------------------------------------------------------- BBCode

// Tags whose content is kept and whose own styling is not: an attribute a
// description supplies never reaches the DOM.
const BB_TRANSPARENT = new Set([
  "size",
  "color",
  "colour",
  "font",
  "center",
  "left",
  "right",
  "justify",
  "spoiler",
]);

const BB_INLINE = {
  b: "strong",
  i: "em",
  u: "u",
  s: "s",
  strike: "s",
};

const BB_BLOCK_KINDS = new Set([
  "p",
  "blockquote",
  "ul",
  "ol",
  "pre",
  "h",
  "hr",
]);

function parseBBCode(s) {
  // A frame is an open tag and what has been collected inside it; the root
  // frame is the document. `raw` is the tag's own source text, restored if
  // the tag is never closed.
  const root = { name: "", raw: "", arg: "", c: [] };
  const stack = [root];
  const top = () => stack[stack.length - 1];
  const re = new RegExp(BB_TAG.source, "gi");
  let last = 0;
  let m;
  while ((m = re.exec(s)) !== null) {
    if (m.index > last) pushAll(top().c, [s.slice(last, m.index)]);
    last = re.lastIndex;
    const [raw, slash, rawName, arg = ""] = m;
    const name = rawName.toLowerCase();

    if (slash) {
      const at = findOpen(stack, name);
      if (at < 0) {
        pushAll(top().c, [raw]); // a stray closing tag is text
        continue;
      }
      // Anything opened inside it and never closed unwinds as text.
      while (stack.length - 1 > at) unwind(stack);
      const frame = stack.pop();
      pushAll(top().c, closeBB(frame, raw));
      continue;
    }

    if (name === "*") {
      // An item runs to the next item or to its list's end.
      const listAt = findOpen(stack, "list");
      if (listAt < 0 || listAt >= MAX_DEPTH) {
        pushAll(top().c, [raw]);
        continue;
      }
      // Whatever the previous item left open ends with it.
      while (stack.length - 1 > listAt) unwind(stack);
      stack.push({ name, raw, arg, c: [] });
      continue;
    }
    if (name === "line") {
      pushAll(top().c, [{ t: "hr" }]);
      continue;
    }
    if (name === "code") {
      // A code block is verbatim: nothing inside it is markup.
      const end = s.toLowerCase().indexOf("[/code]", last);
      if (end < 0) {
        pushAll(top().c, [raw]);
        continue;
      }
      pushAll(top().c, [
        { t: "pre", text: trimBlankLines(s.slice(last, end)) },
      ]);
      last = end + "[/code]".length;
      re.lastIndex = last;
      continue;
    }
    if (stack.length > MAX_DEPTH) {
      pushAll(top().c, [raw]); // nested past the cap: text
      continue;
    }
    stack.push({ name, raw, arg, c: [] });
  }
  if (last < s.length) pushAll(top().c, [s.slice(last)]);
  while (stack.length > 1) unwind(stack);
  return blockify(root.c);
}

function findOpen(stack, name) {
  const want = name === "strike" || name === "s" ? ["s", "strike"] : [name];
  for (let i = stack.length - 1; i > 0; i--) {
    if (want.includes(stack[i].name)) return i;
  }
  return -1;
}

// unwind closes the innermost frame as literal text: its opening tag goes
// back in front of its content. A list item needs no closing tag, and a
// list's items are already structure, so those two close normally.
function unwind(stack) {
  const frame = stack.pop();
  const parent = stack[stack.length - 1];
  if (frame.name === "*" || frame.name === "list") {
    // A list whose [/list] never came still ends where the text does.
    pushAll(parent.c, closeBB(frame, ""));
    return;
  }
  pushAll(parent.c, [frame.raw]);
  pushAll(parent.c, frame.c);
}

// pushAll appends nodes to out, joining adjacent text, one at a time: a
// spread of a long run would overflow the argument stack, and unjoined
// literal tags would make that run long.
function pushAll(out, nodes) {
  for (const n of nodes) {
    if (n === "" || n == null) continue;
    if (typeof n === "string" && typeof out[out.length - 1] === "string") {
      out[out.length - 1] += n;
    } else {
      out.push(n);
    }
  }
}

// closeBB turns a closed frame into the nodes it stands for.
function closeBB(frame, closeRaw) {
  const { name, arg, c } = frame;
  if (BB_INLINE[name]) return [{ t: BB_INLINE[name], c: withBreaks(c) }];
  if (BB_TRANSPARENT.has(name)) return c;
  switch (name) {
    case "url": {
      const target = arg ? arg.replace(/^["']|["']$/g, "") : textOf(c);
      const href = safeHref(target);
      if (!href) return [frame.raw, ...c, closeRaw];
      const label = c.length > 0 ? withBreaks(delink(c)) : [href];
      return [{ t: "a", href, c: label }];
    }
    case "img": {
      const href = safeHref(textOf(c));
      if (!href) return [frame.raw, ...c, closeRaw];
      return [{ t: "img", href, alt: "" }];
    }
    case "youtube": {
      const id = textOf(c).trim();
      if (!/^[A-Za-z0-9_-]{6,20}$/.test(id)) return [frame.raw, ...c, closeRaw];
      const href = `https://www.youtube.com/watch?v=${id}`;
      return [{ t: "a", href, c: [`YouTube video ${id}`] }];
    }
    case "quote":
      return [{ t: "blockquote", c: blockify(c) }];
    case "heading":
    case "h1":
      return [{ t: "h", level: 1, c: inline(c) }];
    case "h2":
      return [{ t: "h", level: 2, c: inline(c) }];
    case "h3":
      return [{ t: "h", level: 3, c: inline(c) }];
    case "list": {
      const ordered = /^[1aAiI]$/.test(arg.trim());
      const items = c.filter((n) => typeof n !== "string" || n.trim());
      const lis = items.map((n) =>
        n && n.t === "li" ? n : { t: "li", c: inline([n]) },
      );
      return [{ t: ordered ? "ol" : "ul", c: lis }];
    }
    case "*":
      return [{ t: "li", c: inline(c) }];
    default:
      return [frame.raw, ...c, closeRaw];
  }
}

// delink flattens the links inside a link's label - an anchor inside an
// anchor is not valid HTML - keeping their words.
function delink(nodes) {
  return nodes.flatMap((n) => {
    if (typeof n === "string") return [n];
    if (n.t === "a") return delink(n.c);
    if (n.t === "img") return [n.alt ? `Image: ${n.alt}` : "Image"];
    return n.c ? [{ ...n, c: delink(n.c) }] : [n];
  });
}

function textOf(nodes) {
  return nodes.map((n) => (typeof n === "string" ? n : "")).join("");
}

// ---------------------------------------------------------------- Markdown

const MD_FENCE = /^[ \t]*```/;
const MD_HEADING = /^[ \t]*(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$/;
const MD_RULE = /^[ \t]*(-{3,}|\*{3,}|_{3,})[ \t]*$/;
const MD_QUOTE = /^[ \t]*>[ \t]?(.*)$/;
const MD_BULLET = /^[ \t]*[-*+][ \t]+(.*)$/;
const MD_NUMBER = /^[ \t]*\d+[.)][ \t]+(.*)$/;

function parseMarkdown(s, depth) {
  const lines = s.split("\n");
  const out = [];
  let para = [];
  const flush = () => {
    if (para.length > 0) out.push({ t: "p", c: mdInlineLines(para) });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (!line.trim()) {
      flush();
      continue;
    }
    if (MD_FENCE.test(line)) {
      const body = [];
      let j = i + 1;
      while (j < lines.length && !MD_FENCE.test(lines[j]))
        body.push(lines[j++]);
      if (j >= lines.length) {
        para.push(line); // an unclosed fence is text
        continue;
      }
      flush();
      out.push({ t: "pre", text: body.join("\n") });
      i = j;
      continue;
    }
    let m = MD_HEADING.exec(line);
    if (m) {
      flush();
      out.push({
        t: "h",
        level: Math.min(m[1].length, 3),
        c: mdInline(m[2]),
      });
      continue;
    }
    if (MD_RULE.test(line)) {
      flush();
      out.push({ t: "hr" });
      continue;
    }
    if (depth < MAX_DEPTH && MD_QUOTE.test(line)) {
      flush();
      const body = [];
      while (i < lines.length && (m = MD_QUOTE.exec(lines[i]))) {
        body.push(m[1]);
        i++;
      }
      i--;
      out.push({
        t: "blockquote",
        c: parseMarkdown(body.join("\n"), depth + 1),
      });
      continue;
    }
    const list = MD_BULLET.test(line)
      ? MD_BULLET
      : MD_NUMBER.test(line)
        ? MD_NUMBER
        : null;
    if (list) {
      flush();
      const items = [];
      while (i < lines.length) {
        const cur = lines[i];
        if ((m = list.exec(cur))) {
          items.push([m[1]]);
        } else if (
          /^[ \t]+\S/.test(cur) &&
          !MD_BULLET.test(cur) &&
          !MD_NUMBER.test(cur)
        ) {
          items[items.length - 1].push(cur.trim()); // a continuation line
        } else if (MD_BULLET.test(cur) || MD_NUMBER.test(cur)) {
          // A nested or differently-marked item: kept flat, in order.
          m = MD_BULLET.exec(cur) ?? MD_NUMBER.exec(cur);
          items.push([m[1]]);
        } else {
          break;
        }
        i++;
      }
      i--;
      out.push({
        t: list === MD_BULLET ? "ul" : "ol",
        c: items.map((item) => ({ t: "li", c: mdInlineLines(item) })),
      });
      continue;
    }
    para.push(line.trim());
  }
  flush();
  return out;
}

// mdInlineLines renders a run of lines, a line break between each: a mod
// description's line breaks are almost always meant.
function mdInlineLines(lines) {
  const out = [];
  lines.forEach((line, i) => {
    if (i > 0) out.push({ t: "br" });
    for (const n of mdInline(line)) out.push(n);
  });
  return out;
}

// One inline construct per alternative, earliest match wins. Group numbers:
// 1 code, 2-3 image, 4-5 link, 6 strong(*), 7 strong(_), 8 strike, 9 em(*),
// 10 em(_), 11-13 a linked image (a README badge: [![alt](img)](link)).
const MD_INLINE =
  /`([^`\n]+)`|(?<!\[)!\[([^\]\n]*)\]\(([^)\s]+)\)|(?<!\[)\[(?!!\[)([^\]\n]+)\]\(([^)\s]+)\)|\*\*(?=\S)([^\n]*?\S)\*\*|__(?=\S)([^\n]*?\S)__|~~(?=\S)([^\n]*?\S)~~|\*(?=[^\s*])([^\n*]*?[^\s*])\*|(?<![A-Za-z0-9])_(?=[^\s_])([^\n_]*?[^\s_])_(?![A-Za-z0-9])|\[!\[([^\]\n]*)\]\(([^)\s]+)\)\]\(([^)\s]+)\)/;

function mdInline(text, inLink = false, depth = 0) {
  if (depth >= MAX_DEPTH) return [text]; // nested past the cap: text
  const out = [];
  let rest = text;
  const inner = (t, link) => mdInline(t, link, depth + 1);
  while (rest) {
    const m = MD_INLINE.exec(rest);
    if (!m) {
      out.push(rest);
      break;
    }
    if (m.index > 0) out.push(rest.slice(0, m.index));
    rest = rest.slice(m.index + m[0].length);
    if (m[1] !== undefined) {
      out.push({ t: "code", c: [m[1]] });
    } else if (m[13] !== undefined) {
      const href = inLink ? "" : safeHref(m[13]);
      out.push(
        href
          ? { t: "a", href, c: [m[11] ? `Image: ${m[11]}` : "Image"] }
          : m[0],
      );
    } else if (m[3] !== undefined) {
      const href = safeHref(m[3]);
      out.push(href ? { t: "img", href, alt: m[2] } : m[0]);
    } else if (m[5] !== undefined) {
      const href = inLink ? "" : safeHref(m[5]);
      out.push(href ? { t: "a", href, c: inner(m[4], true) } : m[0]);
    } else if (m[6] !== undefined || m[7] !== undefined) {
      out.push({ t: "strong", c: inner(m[6] ?? m[7], inLink) });
    } else if (m[8] !== undefined) {
      out.push({ t: "s", c: inner(m[8], inLink) });
    } else {
      out.push({ t: "em", c: inner(m[9] ?? m[10], inLink) });
    }
  }
  return mergeText(out);
}

// ---------------------------------------------------------------- shared

// blockify groups a mixed run of inline and block nodes into paragraphs:
// text splits at blank lines, single newlines become line breaks, and the
// whitespace around a block element is layout, not content.
function blockify(nodes) {
  const out = [];
  let run = [];
  const flush = () => {
    const c = inline(run);
    if (c.length > 0) out.push({ t: "p", c });
    run = [];
  };
  for (const n of nodes) {
    if (typeof n === "string") {
      const parts = n.split(/\n[ \t]*\n\s*/);
      parts.forEach((part, i) => {
        if (i > 0) flush();
        run.push(part);
      });
    } else if (BB_BLOCK_KINDS.has(n.t)) {
      flush();
      out.push(n);
    } else {
      run.push(n);
    }
  }
  flush();
  return out;
}

// inline trims a run's outer whitespace and turns its newlines into breaks;
// a block nested where only inline content fits is kept, not dropped.
function inline(nodes) {
  const merged = mergeText(nodes).map((n, i, all) => {
    if (typeof n !== "string") return n;
    let t = n;
    if (isBlock(all[i + 1])) t = t.replace(/\s+$/, "");
    if (isBlock(all[i - 1])) t = t.replace(/^\s+/, "");
    return t;
  });
  if (typeof merged[0] === "string") merged[0] = merged[0].replace(/^\s+/, "");
  const lastAt = merged.length - 1;
  if (typeof merged[lastAt] === "string") {
    merged[lastAt] = merged[lastAt].replace(/\s+$/, "");
  }
  return withBreaks(merged.filter((n) => n !== ""));
}

function isBlock(n) {
  return n != null && typeof n !== "string" && BB_BLOCK_KINDS.has(n.t);
}

function withBreaks(nodes) {
  const out = [];
  for (const n of mergeText(nodes)) {
    if (typeof n !== "string") {
      out.push(n);
      continue;
    }
    n.split("\n").forEach((part, i) => {
      if (i > 0) out.push({ t: "br" });
      if (part) out.push(part);
    });
  }
  return out;
}

function mergeText(nodes) {
  const out = [];
  for (const n of nodes) {
    if (n === "" || n == null) continue;
    if (typeof n === "string" && typeof out[out.length - 1] === "string") {
      out[out.length - 1] += n;
    } else {
      out.push(n);
    }
  }
  return out;
}

function trimBlankLines(s) {
  return s.replace(/^[ \t]*\n/, "").replace(/\n[ \t]*$/, "");
}

// ---------------------------------------------------------------- render

const PLAIN_TAGS = {
  p: "p",
  blockquote: "blockquote",
  ul: "ul",
  ol: "ol",
  li: "li",
  strong: "strong",
  em: "em",
  u: "u",
  s: "s",
  code: "code",
};

function externalLink(href, children) {
  return h(
    "a",
    {
      class: "richtext__link",
      href,
      rel: "noopener noreferrer",
      target: "_blank",
    },
    children,
    h("span", { class: "richtext__external", "aria-hidden": "true" }, " ↗"),
    h("span", { class: "visually-hidden" }, " (opens in a new tab)"),
  );
}

// RENDER_DEPTH bounds the element tree whatever the parsers hand over; the
// caps above keep a parsed tree well inside it, so this is a backstop.
const RENDER_DEPTH = 96;

// flatText is a subtree's words, gathered without recursion.
function flatText(node) {
  const parts = [];
  const stack = [node];
  while (stack.length > 0) {
    const n = stack.pop();
    if (typeof n === "string") parts.push(n);
    else if (n?.c) for (let i = n.c.length - 1; i >= 0; i--) stack.push(n.c[i]);
    else if (n?.text) parts.push(n.text);
  }
  return parts.join("");
}

function renderNode(n, key, depth = 0) {
  if (typeof n === "string") return n;
  if (depth >= RENDER_DEPTH) return flatText(n);
  const kids = () => (n.c ?? []).map((c, i) => renderNode(c, i, depth + 1));
  if (PLAIN_TAGS[n.t]) return h(PLAIN_TAGS[n.t], { key }, kids());
  switch (n.t) {
    case "h":
      return h(`h${n.level + 2}`, { key, class: "richtext__heading" }, kids());
    case "pre":
      return h(
        "pre",
        { key, class: "richtext__code" },
        h("code", null, n.text),
      );
    case "a":
      return externalLink(n.href, kids());
    case "img":
      return externalLink(n.href, [
        h(
          "span",
          { class: "richtext__image" },
          n.alt ? `Image: ${n.alt}` : "Image",
        ),
      ]);
    case "br":
      return h("br", { key });
    case "hr":
      return h("hr", { key, class: "richtext__rule" });
    default:
      return null; // not on the allow-list: nothing renders
  }
}

/**
 * renderRichText maps parseRichText's nodes onto allow-listed elements.
 */
export function renderRichText(nodes) {
  return nodes.map((n, i) => renderNode(n, i));
}

/**
 * RichText renders a description string, detected and parsed, inside a
 * container that carries the detected family for styling and for tests.
 */
export function RichText({ text, class: className = "" }) {
  const { family, nodes } = parseRichText(text);
  if (nodes.length === 0) return null;
  return h(
    "div",
    { class: `richtext ${className}`.trim(), "data-family": family },
    renderRichText(nodes),
  );
}
