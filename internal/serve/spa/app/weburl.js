// weburl.js - the one rule for when a URL a source supplied may become a link.
//
// A mod's page URL is DATA: a source (or a custom manifest's author) wrote
// it. Only an absolute http or https address is ever rendered as a link, so a
// javascript:, data: or file: URL - or a relative one, which would resolve
// against lmm's own origin - never reaches an href. This is the browser half
// of domain.SafeWebURL (internal/domain/weburl.go); core applies the same
// rule before it puts a page URL in a typed download error, and the two are
// kept in step by e2e_mod_page_link_test.go.

/**
 * safeWebUrl returns raw as a normalised absolute http(s) URL, or "" when it
 * is anything else - missing, not a string, unparseable, relative, or another
 * scheme. The returned string is the PARSED form (URL#href), so what lands in
 * an href is what the browser understood, not the author's spelling of it.
 */
export function safeWebUrl(raw) {
  if (typeof raw !== "string") return "";
  const trimmed = raw.trim();
  // The WHATWG parser is lenient where Go's is not: it reads "https:///mod"
  // as host "mod" and "https:example.test" as https://example.test/. Only an
  // address that spells out scheme://host is a link, same as domain.SafeWebURL.
  if (!/^https?:\/\/[^/?#\s]/i.test(trimmed)) return "";
  let url;
  try {
    url = new URL(trimmed);
  } catch {
    return "";
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return "";
  if (url.host === "") return "";
  return url.href;
}
