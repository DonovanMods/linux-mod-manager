// Package thunderstore: this file is the remote half - one request, for one
// document, through the shared httpclient transport with the house
// retry/backoff RoundTripper in front of it.
//
// Everything here is a READ of a public document. No credential exists for
// this source, so nothing here has one to send.
package thunderstore

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// communityPackagesPath is the only public listing endpoint Thunderstore
// has. It is UNPAGINATED - ?page=2 returns the whole document byte for byte
// (measured) - and the endpoint that does paginate answers 403, which is
// why this source keeps a local index at all.
func communityPackagesPath(community string) string {
	return "/c/" + community + "/api/v1/package/"
}

// fetchTimeout bounds one community fetch end to end. Generous on purpose:
// the largest community on the site is 34.6 MB on the wire, and a user on a
// slow link must still get an index rather than a timeout.
const fetchTimeout = 10 * time.Minute

// maxIndexBytes is the ceiling on ONE community document, measured on the
// stream the decoder reads - which net/http has already decompressed, so
// the number to compare it against is the 329 MB of JSON behind the site's
// largest community's 34.6 MB of gzip, not the 34.6 MB.
//
// 512 MiB is that with about half again in headroom. It exists because
// this is the one response in lmm written straight to DISK as it arrives
// (T1 review #6): the ten-minute fetchTimeout alone bounds a hostile or
// broken upstream at tens of gigabytes on a domestic link, and the failure
// would be a full filesystem rather than the out-of-memory a buffered
// source would hit. Over it, the build fails with ErrIndexUnavailable and
// the previous index - if any - is untouched, exactly as any other failed
// refresh.
const maxIndexBytes = 512 << 20

// client is the Thunderstore half of the source: one httpclient over a
// retrying transport.
type client struct {
	http *apiClient
	// baseURL is the host every request is built against, kept here because
	// ONE url this source produces is handed back to a caller to fetch
	// rather than issued here: a version's download URL (#409 §3.3). It has
	// to come from the same place the index did, or a test would have no
	// way to serve it.
	baseURL string
}

func newClient(opts Options, now func() time.Time) *client {
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: fetchTimeout}
	}
	// Retry and circuit-breaking live in the transport rather than around
	// the call: only that layer sees Retry-After, and only that layer can
	// retry BEFORE the streaming decode has started reading the body.
	retrying := *httpClient
	retrying.Transport = newRetryTransport(httpClient.Transport, now)
	limit := opts.MaxIndexBytes
	if limit <= 0 {
		limit = maxIndexBytes
	}
	return &client{http: newAPIClient(&retrying, baseURL, limit), baseURL: strings.TrimSuffix(baseURL, "/")}
}

// fetchCommunity issues the conditional GET. ifModifiedSince is the
// watermark's stored Last-Modified, or "" for an unconditional fetch; a 304
// comes back as a response, not an error (httpclient.DoStream's contract),
// and the caller stamps its watermark rather than reading a body.
//
// The response body is the CALLER's to read and close.
func (c *client) fetchCommunity(ctx context.Context, community, ifModifiedSince string) (*http.Response, error) {
	var header map[string]string
	if ifModifiedSince != "" {
		header = map[string]string{"If-Modified-Since": ifModifiedSince}
	}
	resp, err := c.http.DoStream(ctx, http.MethodGet, communityPackagesPath(community), header)
	if err != nil {
		return nil, fmt.Errorf("fetching the %s package index: %w", community, err)
	}
	return resp, nil
}

// apiClient is the shared httpclient, aliased so this package's own
// constructor can document the one odd thing about it: httpclient.New
// requires an auth field, and this source has no credential of any kind.
type apiClient = httpclient.Client

// newAPIClient builds the one client this package uses. AuthHeader is
// declared only to satisfy New's required-field check - APIKey is never
// set, so applyAuthHeader never sends the header and nothing here can leak
// a credential lmm does not have.
func newAPIClient(doer *http.Client, baseURL string, maxBytes int64) *apiClient {
	return httpclient.New(httpclient.Options{
		HTTPClient:       doer,
		BaseURL:          baseURL,
		AuthHeader:       "authorization",
		AuthLabel:        "Thunderstore",
		MaxResponseBytes: maxBytes,
	})
}
