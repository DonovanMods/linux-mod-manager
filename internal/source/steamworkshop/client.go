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
var ErrItemUnavailable = errors.New("steam workshop item is unavailable (delisted, deleted or private)")

// ErrMetadataUnavailable reports that Valve's API could not be reached at
// all - a transport failure, an exhausted retry budget, or the circuit
// breaker holding calls off after repeated failures. It is deliberately
// distinct from ErrItemUnavailable: an item lmm could not ASK about must
// never be reported as "up to date".
var ErrMetadataUnavailable = errors.New("steam workshop metadata is unavailable")

// client is the Steam Web API half of the source: one httpclient, one
// on-disk metadata cache, one clock.
type client struct {
	http  *httpclient.Client
	cache *metaCache
	now   func() time.Time
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
		http: httpclient.New(httpclient.Options{
			HTTPClient: &retrying,
			BaseURL:    baseURL,
			// Tier 1 is keyless; the parameter is declared so Tier 2's
			// bring-your-own key attaches with no client change, and so
			// httpclient.New's required-field check is satisfied without
			// pretending Steam takes a header.
			AuthQueryParam: "key",
			AuthLabel:      "Steam",
		}),
		cache: newMetaCache(opts.CacheDir, now),
		now:   now,
	}
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
	PublishedFileID       string    `json:"publishedfileid"`
	Result                int       `json:"result"`
	Creator               string    `json:"creator"`
	Title                 string    `json:"title"`
	Description           string    `json:"description"`
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
		Summary:     d.Description,
		Description: d.Description,
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
