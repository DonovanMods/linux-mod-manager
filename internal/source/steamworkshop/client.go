// Package steamworkshop: this file is the remote half - Valve's keyless
// GetPublishedFileDetails endpoint, batched, cached and backed off.
//
// Everything here is READ-ONLY metadata about published files. No call
// here is authenticated, none of it touches the user's Steam account, and
// none of it downloads content.
package steamworkshop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

// DefaultBaseURL is Valve's Web API host. Tests must override it with an
// httptest server (Options.BaseURL); TestNoTestReachesTheProductionAPI
// enforces that no test in this package names the real host at all.
const DefaultBaseURL = "https://api.steampowered.com"

// publishedFileDetailsPath is the keyless batch-metadata endpoint. It takes
// a form POST, not a query string, and no key parameter is documented for
// it - which is exactly why Tier 1 needs no credential of any kind.
const publishedFileDetailsPath = "/ISteamRemoteStorage/GetPublishedFileDetails/v1/"

// maxIDsPerRequest caps one GetPublishedFileDetails batch. Valve documents
// no hard limit; 100 is the conventional ceiling every Steam client library
// uses and keeps a single failed request cheap to retry.
const maxIDsPerRequest = 100

// ErrItemUnavailable reports an item Valve will not describe: the spike's
// live-observed `result: 9` (file not found), which a delisted, deleted or
// private item returns while still appearing in a user's own ACF. It is
// handled PER ITEM, never per response - one dead item in a batch of thirty
// must not blind the other twenty-nine.
//
// It IS domain.ErrWorkshopItemUnavailable, not a second error that means
// the same thing: steamcmd's "(Access Denied)" (Tier 3, steamcmd.go) and
// this API result are one fact about the item, and a caller must not have
// to know which route the answer came back by.
var ErrItemUnavailable = domain.ErrWorkshopItemUnavailable

// ErrMetadataUnavailable reports that Valve's API could not be reached at
// all - a transport failure, an exhausted retry budget, or the circuit
// breaker holding calls off after repeated failures. It is deliberately
// distinct from ErrItemUnavailable: an item lmm could not ASK about must
// never be reported as "up to date".
var ErrMetadataUnavailable = errors.New("steam workshop metadata is unavailable")

// client is the Steam Web API half of the source: one httpclient, one
// on-disk metadata cache, one in-memory search cache, one clock.
//
// doer and baseURL are kept alongside the ready-made http client so a
// SECOND client can be built for one call against a key that is not the
// registered one - which is exactly what ValidateKey needs, and the only
// way to avoid a request carrying two different key= parameters.
type client struct {
	http    *httpclient.Client
	doer    *http.Client
	baseURL string
	cache   *metaCache
	search  *searchCache
	now     func() time.Time

	// keyID identifies the registered API key WITHOUT being it: the first
	// 8 hex of its SHA-256, enough for the search cache to tell two
	// credentials apart and useless to anything else. SetAPIKey maintains
	// it; nothing reads the plaintext back out of this struct.
	keyID string
}

func newClient(opts Options) *client {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	// Retry and circuit-breaking live in the transport rather than around
	// each call: a RoundTripper can rewind the request body (GetBody) and
	// is the only layer that sees the Retry-After header at all.
	retrying := *httpClient
	retrying.Transport = newRetryTransport(httpClient.Transport, now)

	return &client{
		http:    newAPIClient(&retrying, baseURL, ""),
		doer:    &retrying,
		baseURL: baseURL,
		cache:   newMetaCache(opts.CacheDir, now),
		search:  newSearchCache(now),
		now:     now,
	}
}

// newAPIClient builds one httpclient against Valve's API. The key travels
// as a QUERY PARAMETER because that is the only form Steam's Web API takes;
// declaring it keeps httpclient.New's required-field check satisfied for
// the keyless Tier-1 endpoints too, without pretending Steam reads a header.
func newAPIClient(doer *http.Client, baseURL, key string) *httpclient.Client {
	return httpclient.New(httpclient.Options{
		HTTPClient:     doer,
		BaseURL:        baseURL,
		APIKey:         key,
		AuthQueryParam: "key",
		AuthLabel:      "Steam",
		ErrorMapper:    mapSteamError,
	})
}

// keyed returns a one-call client authenticated with key instead of the
// registered one, sharing this client's transport (and so its retry budget
// and circuit breaker). ValidateKey is its only caller: probing a CANDIDATE
// key through the registered client would either append a second key=
// parameter to the URL or overwrite a working credential with one the user
// has not committed to yet.
func (c *client) keyed(key string) *httpclient.Client {
	return newAPIClient(c.doer, c.baseURL, key)
}

// mapSteamError translates the one non-2xx status Valve uses to mean
// "your key is missing or wrong". The Web API answers 403 (not 401) with
// "Please verify your key= parameter", live-verified by the #268 spike, so
// httpclient's own 401 rule never fires for this source; mapping it here
// is what lets every caller branch on domain.ErrAuthRequired as they do
// for every other source.
//
// The RESPONSE body is deliberately not interpolated: it is Valve's HTML
// error page on some paths, and the request URL it can echo carries the
// key.
func mapSteamError(status int, _ []byte, _ string) error {
	if status == http.StatusForbidden {
		return fmt.Errorf("%w: Steam Web API key required (run `lmm auth login steamworkshop`)", domain.ErrAuthRequired)
	}
	return nil
}

// flexInt64 decodes a field Valve reports sometimes as a JSON number and
// sometimes as a quoted string (file_size is the observed offender), so one
// struct handles both without a second response type.
type flexInt64 int64

// UnmarshalJSON accepts a number or a decimal string; anything else, and
// anything that will not parse, reads as 0 - a size lmm cannot read is a
// missing fact, not a reason to fail a whole batch.
func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt64(n)
	return nil
}

// itemDetails is one publishedfiledetails row, in the subset lmm uses.
type itemDetails struct {
	PublishedFileID string `json:"publishedfileid"`
	Result          int    `json:"result"`
	Creator         string `json:"creator"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	// FileDescription is the SAME fact under the name IPublishedFileService
	// gives it: GetPublishedFileDetails answers `description`, QueryFiles
	// answers `file_description`. Carrying both on one struct is what lets
	// Tier 1 and Tier 2 share modFromDetails rather than grow a second
	// mapping that could drift from it (#269 W2).
	FileDescription       string    `json:"file_description"`
	FileSize              flexInt64 `json:"file_size"`
	FileURL               string    `json:"file_url"`
	HContentFile          string    `json:"hcontent_file"`
	PreviewURL            string    `json:"preview_url"`
	TimeCreated           int64     `json:"time_created"`
	TimeUpdated           int64     `json:"time_updated"`
	LifetimeSubscriptions int64     `json:"lifetime_subscriptions"`
	Tags                  []struct {
		Tag string `json:"tag"`
	} `json:"tags"`
}

// describedText is the item's description under whichever of Valve's two
// names this response used. It stays RAW source markup, per the accepted
// #86 precedent - see modFromDetails.
func (d itemDetails) describedText() string {
	if d.Description != "" {
		return d.Description
	}
	return d.FileDescription
}

// available reports whether Valve actually described this item. `result: 1`
// is success; anything else (the spike observed 9) means "no such file, as
// far as this API is concerned".
func (d itemDetails) available() bool { return d.Result == 1 }

// detailsResponse is GetPublishedFileDetails' envelope.
type detailsResponse struct {
	Response struct {
		Result      int           `json:"result"`
		ResultCount int           `json:"resultcount"`
		Details     []itemDetails `json:"publishedfiledetails"`
	} `json:"response"`
}

// fetchDetails resolves every id to its metadata, serving what the cache
// still considers fresh and asking Valve for the rest in batches of
// maxIDsPerRequest. refresh bypasses the cache for reads while still
// writing the fresh answers back.
//
// The returned map holds one entry per id lmm got an ANSWER for, including
// unavailable ones (Result != 1) - a caller distinguishes them with
// itemDetails.available(). An id missing from the map is one the API did
// not mention at all.
//
// A transport failure is returned as ErrMetadataUnavailable so a caller can
// say "Steam metadata unavailable" instead of "up to date"; per-item
// unavailability is never an error here.
func (c *client) fetchDetails(ctx context.Context, ids []string, refresh bool) (map[string]itemDetails, error) {
	out := make(map[string]itemDetails, len(ids))
	var missing []string
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if !refresh {
			if cached, ok := c.cache.get(id); ok {
				out[id] = cached
				continue
			}
		}
		missing = append(missing, id)
	}

	for start := 0; start < len(missing); start += maxIDsPerRequest {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		end := min(start+maxIDsPerRequest, len(missing))
		batch := missing[start:end]

		form := formForIDs(batch)
		var resp detailsResponse
		if err := c.http.DoForm(ctx, publishedFileDetailsPath, form, &resp); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return out, ctxErr
			}
			return out, fmt.Errorf("%w: %v", ErrMetadataUnavailable, err)
		}
		for _, d := range resp.Response.Details {
			if d.PublishedFileID == "" {
				continue
			}
			out[d.PublishedFileID] = d
			c.cache.put(d)
		}
	}
	return out, nil
}

// formForIDs builds GetPublishedFileDetails' request body: an itemcount and
// one indexed publishedfileids[i] per id, which is the only shape the
// endpoint accepts.
func formForIDs(ids []string) url.Values {
	form := url.Values{}
	form.Set("itemcount", strconv.Itoa(len(ids)))
	for i, id := range ids {
		form.Set("publishedfileids["+strconv.Itoa(i)+"]", id)
	}
	return form
}

// detailsFor resolves exactly one item, mapping the two "no answer" cases
// onto their sentinels so callers need no map bookkeeping of their own.
func (c *client) detailsFor(ctx context.Context, fileID string, refresh bool) (itemDetails, error) {
	got, err := c.fetchDetails(ctx, []string{fileID}, refresh)
	if err != nil {
		return itemDetails{}, err
	}
	d, ok := got[fileID]
	if !ok {
		return itemDetails{}, fmt.Errorf("%w: %s", ErrItemUnavailable, fileID)
	}
	if !d.available() {
		return itemDetails{}, fmt.Errorf("%w: %s (result %d)", ErrItemUnavailable, fileID, d.Result)
	}
	return d, nil
}

// GetMod returns one Workshop item's metadata as a domain.Mod.
//
// gameID is the Steam app id (unused by the endpoint, which resolves a
// published file globally, but carried onto the mod so the row records
// which game it belongs to). Author is the RAW creator steamid64: resolving
// it to a display name needs GetPlayerSummaries, i.e. a key, and SourceURL
// is one click from the real name.
func (s *Source) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	d, err := s.client.detailsFor(ctx, modID, false)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", sourceID, err)
	}
	mod := modFromDetails(d, gameID)
	return &mod, nil
}

// modFromDetails is the one mapping from Valve's published-file shape to
// domain.Mod, shared by GetMod and (Tier 2) search.
//
// Description stays RAW source markup all the way to --json, per the
// accepted #86 precedent - every renderer downstream must keep treating it
// as untrusted text. Version is the content id (hcontent_file): the only
// stable, comparable identity a Workshop item has, and what the ACF records
// too, which is what makes the update comparison exact.
func modFromDetails(d itemDetails, gameID string) domain.Mod {
	mod := domain.Mod{
		ID:          d.PublishedFileID,
		SourceID:    sourceID,
		Name:        d.Title,
		Version:     contentVersion(d),
		Author:      d.Creator,
		Summary:     d.describedText(),
		Description: d.describedText(),
		GameID:      gameID,
		Downloads:   d.LifetimeSubscriptions,
		PictureURL:  d.PreviewURL,
		SourceURL:   SourceURL(d.PublishedFileID),
	}
	if len(d.Tags) > 0 {
		mod.Category = d.Tags[0].Tag
	}
	if d.TimeUpdated > 0 {
		mod.UpdatedAt = time.Unix(d.TimeUpdated, 0).UTC()
	}
	return mod
}

// contentVersion is an item's version identity as the API reports it: the
// content id, or "" when the API reports none (the observed shape is a
// literal "0"). An empty identity is what sends the update check to its
// secondary, timestamp-based signal.
func contentVersion(d itemDetails) string {
	if d.HContentFile == "" || d.HContentFile == "0" {
		return ""
	}
	return d.HContentFile
}
