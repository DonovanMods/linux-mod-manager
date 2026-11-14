package domain

import (
	"net/url"
	"strings"
)

// SafeWebURL returns raw, trimmed, when it is an absolute http or https URL
// with a host, and "" otherwise. It is the one rule every frontend applies
// before it shows a mod's page as a link or a "visit" line: a source URL is
// data a source (or a custom manifest's author) supplied, so a javascript:,
// data: or file: URL, a relative one, or an unparseable one must never reach
// an href or a terminal as something to open.
func SafeWebURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return raw
}

// PageURL is the mod's page on its source as SafeWebURL allows it: empty
// when the source gave none or gave one that is not a plain web address.
func (m *Mod) PageURL() string {
	if m == nil {
		return ""
	}
	return SafeWebURL(m.SourceURL)
}

// RedactURL is raw as lmm shows or logs it (#622): an absolute http or
// https address without its userinfo (credentials) or fragment, and with
// any query string replaced by "?…", since a query can carry a token. It
// is "" when raw is not such an address.
func RedactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	query := u.RawQuery != "" || u.ForceQuery
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	if query {
		return u.String() + "?…"
	}
	return u.String()
}
