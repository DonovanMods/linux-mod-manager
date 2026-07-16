package custom

import "net/url"

// sameOriginURLs reports whether two URLs share scheme and host. Ports are
// normalized before comparing: an explicit default port (:443 on https, :80
// on http) matches a URL with no port at all; any other explicit port must
// match exactly. Either URL failing to parse is not same-origin (fail closed).
// Used to scope custom-source API keys to their own origin (design §9).
func sameOriginURLs(a, b string) bool {
	au, err := url.Parse(a)
	if err != nil {
		return false
	}
	bu, err := url.Parse(b)
	if err != nil {
		return false
	}
	return au.Scheme == bu.Scheme && normalizedHost(au) == normalizedHost(bu)
}

// normalizedHost returns u's hostname, with an explicit port stripped when
// it is the scheme's default (443 for https, 80 for http). This lets
// sameOrigin treat a bare host and that same host with its default port
// spelled out as identical origins.
func normalizedHost(u *url.URL) string {
	port := u.Port()
	if port == "" {
		return u.Hostname()
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return u.Hostname()
	}
	return u.Hostname() + ":" + port
}
