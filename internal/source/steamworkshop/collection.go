// Package steamworkshop: this file is Tier 2's collection half (#269 W2) -
// Valve's keyless GetCollectionDetails endpoint, and the two parsers that
// turn what a user pastes into a published-file id.
//
// A Workshop COLLECTION is a mod list, which is exactly what an lmm profile
// is. So a collection is not a search facet here (a row nobody can install
// as a unit); it is an INPUT to `lmm profile import`, and core assembles a
// profile document out of the ids this file resolves.
package steamworkshop

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// collectionDetailsPath is the keyless collection endpoint. Like
// GetPublishedFileDetails it is a form POST, and like it needs no key at
// all - a collection import therefore WORKS for a user who has never run
// `lmm auth login steamworkshop`, even though it ships in the tier where
// the client code lives.
const collectionDetailsPath = "/ISteamRemoteStorage/GetCollectionDetails/v1/"

var _ source.CollectionResolver = (*Source)(nil)

// publishedFileID matches the decimal id Steam gives every published file.
// The bound is deliberately loose at both ends: ids were 8 digits in 2012
// and are 10 now, and lmm has no business predicting when they grow again.
var publishedFileID = regexp.MustCompile(`^[0-9]{6,20}$`)

// collectionResponse is GetCollectionDetails' envelope.
type collectionResponse struct {
	Response struct {
		Result      int `json:"result"`
		ResultCount int `json:"resultcount"`
		Collections []struct {
			PublishedFileID string `json:"publishedfileid"`
			Result          int    `json:"result"`
			Children        []struct {
				PublishedFileID string `json:"publishedfileid"`
				SortOrder       int    `json:"sortorder"`
				FileType        int    `json:"filetype"`
			} `json:"children"`
		} `json:"collectiondetails"`
	} `json:"response"`
}

// ResolveCollection implements source.CollectionResolver: it turns a
// collection id (or a URL carrying one) into the published-file ids it
// contains, in the order Valve lists them.
//
// The order matters and is preserved: a collection author sequences a mod
// list, and that sequence becomes the imported profile's load order.
func (s *Source) ResolveCollection(ctx context.Context, ref string) (source.Collection, error) {
	id, ok := parseCollectionID(ref)
	if !ok {
		return source.Collection{}, fmt.Errorf("source %q: %w: %q is not a Steam Workshop collection id or URL",
			sourceID, source.ErrInvalidReference, strings.TrimSpace(ref))
	}

	form := url.Values{}
	form.Set("collectioncount", "1")
	form.Set("publishedfileids[0]", id)

	var resp collectionResponse
	if err := s.client.http.DoForm(ctx, collectionDetailsPath, form, &resp); err != nil {
		return source.Collection{}, fmt.Errorf("source %q: resolving collection %s: %w", sourceID, id, err)
	}
	// A collection Valve will not describe is the REFERENCE being wrong from
	// the caller's point of view, so both sentinels are carried:
	// ErrItemUnavailable says what happened, ErrInvalidReference says whose
	// fault it is.
	if len(resp.Response.Collections) == 0 {
		return source.Collection{}, fmt.Errorf("source %q: %w: %w: collection %s",
			sourceID, source.ErrInvalidReference, ErrItemUnavailable, id)
	}
	got := resp.Response.Collections[0]
	if got.Result != 1 {
		return source.Collection{}, fmt.Errorf("source %q: %w: %w: collection %s (result %d)",
			sourceID, source.ErrInvalidReference, ErrItemUnavailable, id, got.Result)
	}

	out := source.Collection{ID: id, URL: SourceURL(id)}
	for _, child := range got.Children {
		if child.PublishedFileID == "" {
			continue
		}
		out.ItemIDs = append(out.ItemIDs, child.PublishedFileID)
	}
	if len(out.ItemIDs) == 0 {
		return source.Collection{}, fmt.Errorf("source %q: collection %s: %w: %w",
			sourceID, id, source.ErrInvalidReference, errEmptyCollection)
	}
	// GetCollectionDetails carries no title of its own; the collection's
	// NAME is an ordinary published file's, which the metadata endpoint
	// answers. It is one extra keyless call for a user-facing string, made
	// best-effort: a collection lmm can list but not name is still fully
	// importable.
	if d, err := s.client.detailsFor(ctx, id, false); err == nil {
		out.Name = d.Title
	}
	return out, nil
}

// errEmptyCollection is a collection Valve describes but that contains
// nothing - importable in principle, useless in practice, and worth saying
// so rather than producing a profile with no mods in it.
var errEmptyCollection = errors.New("the collection is empty")

// parseCollectionID accepts either form a caller of `lmm profile import
// --workshop-collection` may hand over: the bare decimal id, or any Steam
// Community URL carrying it as ?id=. It is deliberately permissive about
// which community path the URL uses (sharedfiles/ and workshop/ both
// appear in the wild, and both are what a browser copies).
func parseCollectionID(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if publishedFileID.MatchString(ref) {
		return ref, true
	}
	return ParseCollectionURL(ref)
}

// ParseCollectionURL reports whether text is unmistakably a Steam Workshop
// item URL, and returns the published-file id it names.
//
// It is the NARROW parser, and exists for one caller: the web UI's search
// box, which offers "import this collection" when what was typed is a
// Workshop link. A bare id must not qualify there - it is an entirely
// ordinary search term - so only parseCollectionID, whose caller has
// already said "this is a collection", accepts one.
func ParseCollectionURL(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
		return "", false
	}
	u, err := url.Parse(text)
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "steamcommunity.com" {
		return "", false
	}
	id := u.Query().Get("id")
	if !publishedFileID.MatchString(id) {
		return "", false
	}
	return id, true
}
