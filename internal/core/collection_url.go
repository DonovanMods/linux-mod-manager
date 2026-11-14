// collection_url.go is the URL half of the lmm collection import (#622;
// design §12, spec docs/collection-format.md): `lmm profile import
// https://…` and the web UI's import field. Core fetches the document,
// sniffs it by its first bytes - a `.lmmc` container, or YAML of version 1
// or version 0 - and plans it as PlanCollectionImport does, with two
// differences that are structural rather than options:
//
//   - its hooks are listed and never kept: the plan's profile holds none,
//     so ProfileImportOptions.WithHooks has nothing to switch on, and the
//     result counts them as withheld;
//   - the address is recorded as the profile's collection origin
//     (origin.import_url), which a collection update (#613) fetches again.
//
// The fetch is the download client's, over https only: a redirect to
// anything else is refused before it is requested, the body is capped as
// it arrives (a container at maxCollectionContainer, a YAML document at
// maxCollectionDocument), and the whole fetch has a deadline. An address's
// userinfo is used for that one request and never stored; it and the query
// string are cut from everything lmm shows or logs (domain.RedactURL).
package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// collectionFetchTimeout bounds a whole URL fetch: a document is at most
// a few megabytes, and a container at most maxCollectionContainer.
const collectionFetchTimeout = 2 * time.Minute

// maxCollectionRedirects is how many redirects a fetch follows.
const maxCollectionRedirects = 10

// CollectionURLImportRequest is what PlanCollectionImportURL plans.
type CollectionURLImportRequest struct {
	// URL is the https address of the document: a `.lmmc` container, or a
	// YAML document of version 1 or 0.
	URL string
	// GameID names the game to import into (`--game`). Empty matches the
	// document's portable game identity, or a version-0 export's own
	// game_id, against the configured games.
	GameID string
	// ProfileName names the profile to create (`--as`). Empty uses the
	// document's own name.
	ProfileName string
}

// ErrCollectionURL refuses an import address lmm does not fetch from: not
// an absolute https URL with a host. It is the caller's input.
var ErrCollectionURL = errors.New("not an address lmm imports a collection from")

// IsCollectionURLInvalid reports whether err refused the import address
// itself (ErrCollectionURL).
func IsCollectionURLInvalid(err error) bool { return errors.Is(err, ErrCollectionURL) }

// ErrCollectionFetch is a URL import whose fetch failed: the address did
// not answer, answered with an error status, or redirected off https.
var ErrCollectionFetch = errors.New("fetching the collection failed")

// IsCollectionFetchFailed reports whether err is a URL import's failed
// fetch (ErrCollectionFetch).
func IsCollectionFetchFailed(err error) bool { return errors.Is(err, ErrCollectionFetch) }

// errCollectionRedirectNotHTTPS refuses a redirect off https.
var errCollectionRedirectNotHTTPS = errors.New("the address redirected to a plain-http (or other non-https) address, which lmm does not follow")

// IsCollectionURL reports whether arg names a web address rather than a
// file: it starts with http:// or https://, in any case. A frontend routes
// on it; core then refuses anything but https, saying why.
func IsCollectionURL(arg string) bool {
	lower := strings.ToLower(strings.TrimSpace(arg))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// PlanCollectionImportURL fetches the collection document at req.URL and
// plans importing the profile it describes, as PlanCollectionImport does
// for a file (Input CollectionFromURL). Nothing is written. The address,
// without its userinfo and fragment, is the imported profile's
// origin.import_url; the plan shows it redacted.
func (s *Service) PlanCollectionImportURL(ctx context.Context, req CollectionURLImportRequest) (*ImportPlan, error) {
	u, err := parseCollectionURL(req.URL)
	if err != nil {
		return nil, err
	}
	data, err := s.fetchCollectionDocument(ctx, u)
	if err != nil {
		return nil, err
	}
	var warnings []string
	if u.User != nil {
		warnings = append(warnings, "the address's credentials were used for this download only and are not recorded: a later update of this collection fetches the address without them")
	}
	recorded := *u
	recorded.User = nil
	return s.planCollectionImport(ctx, CollectionImportRequest{
		Data: data, Input: CollectionFromURL, GameID: req.GameID, ProfileName: req.ProfileName,
	}, recorded.String(), warnings)
}

// parseCollectionURL checks raw is an absolute https address with a host,
// and returns it without its fragment (which never reaches a server).
func parseCollectionURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: no address given", ErrCollectionURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: it is not a URL", ErrCollectionURL)
	}
	shown := domain.RedactURL(raw)
	if shown == "" {
		shown = "the address"
	}
	switch {
	case u.Scheme == "http":
		return nil, fmt.Errorf("%w: %s is plain http; lmm fetches a collection over https only", ErrCollectionURL, shown)
	case u.Scheme != "https":
		return nil, fmt.Errorf("%w: only an https address imports (or give a local file's path)", ErrCollectionURL)
	case u.Opaque != "" || u.Host == "" || u.Hostname() == "":
		return nil, fmt.Errorf("%w: %s has no host", ErrCollectionURL, shown)
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// fetchCollectionDocument GETs u and returns its body: a `.lmmc`
// container up to maxCollectionContainer, or anything else - a YAML
// document - up to maxCollectionDocument, decided by the first bytes and
// enforced as the body arrives. Nothing it returns quotes u's userinfo or
// query.
func (s *Service) fetchCollectionDocument(ctx context.Context, u *url.URL) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, collectionFetchTimeout)
	defer cancel()
	shown := domain.RedactURL(u.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCollectionURL, shown, err)
	}
	client := *s.downloader.httpClient
	base := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" {
			return errCollectionRedirectNotHTTPS
		}
		if len(via) >= maxCollectionRedirects {
			return fmt.Errorf("stopped after %d redirects", maxCollectionRedirects)
		}
		if base != nil {
			return base(next, via)
		}
		return nil
	}
	s.logger().Debug("fetching a collection", "url", shown)
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error's text quotes the address that failed - after a
		// redirect, one the user never typed. Report it redacted.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			where := domain.RedactURL(uerr.URL)
			if where == "" {
				where = shown
			}
			return nil, fmt.Errorf("%w: requesting %s: %w", ErrCollectionFetch, where, uerr.Err)
		}
		return nil, fmt.Errorf("%w: requesting %s: %w", ErrCollectionFetch, shown, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered HTTP %s", ErrCollectionFetch, shown, statusText(resp))
	}
	if resp.ContentLength > maxCollectionContainer {
		return nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit for a collection", ErrInvalidCollection, shown, resp.ContentLength, maxCollectionContainer)
	}

	// The first bytes decide the cap: only a container may be large.
	head := make([]byte, len(zipMagic))
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrCollectionFetch, shown, err)
	}
	head = head[:n]
	limit, what := int64(maxCollectionDocument), "document"
	if IsCollectionContainer(head) {
		limit, what = maxCollectionContainer, "container"
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%w: the %s at %s is %d bytes, over the %d-byte limit", ErrInvalidCollection, what, shown, resp.ContentLength, limit)
	}
	data, err := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(head), resp.Body), limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrCollectionFetch, shown, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: the %s at %s is over the %d-byte limit", ErrInvalidCollection, what, shown, limit)
	}
	return data, nil
}

// readCollectionData decodes data, a collection import's bytes: a `.lmmc`
// container or a version-1 YAML document, each within its cap. allowV0
// also takes a version-0 profile export (a URL import, #622), lifted into
// a document of FormatVersion 0 - the collection blocks a version-0
// document never carries stay empty, as they do on a file's version-0
// import.
func (s *Service) readCollectionData(data []byte, allowV0 bool) (doc *domain.CollectionDocument, container bool, warnings []string, err error) {
	raw := data
	container = IsCollectionContainer(raw)
	if container {
		inner, w, err := readCollectionContainer(raw)
		if err != nil {
			return nil, false, nil, err
		}
		raw, warnings = inner, w
	}
	if len(raw) > maxCollectionDocument {
		return nil, false, nil, fmt.Errorf("%w: the document is %d bytes, over the %d-byte limit", ErrInvalidCollection, len(raw), maxCollectionDocument)
	}
	if allowV0 && !container && !IsCollectionDocument(raw) {
		doc, err := liftProfileExport(raw)
		return doc, false, nil, err
	}
	version, err := config.CollectionFormatOf(raw)
	if err != nil {
		return nil, false, nil, fmt.Errorf("%w: %v", ErrInvalidCollection, err)
	}
	switch {
	case version < 1:
		return nil, false, nil, fmt.Errorf("%w: it has no lmm_collection version (a version-0 profile export imports as one)", ErrInvalidCollection)
	case version > domain.CollectionFormatVersion:
		return nil, false, nil, fmt.Errorf("%w: it is format version %d, and this lmm reads version %d; update lmm",
			ErrCollectionFormatTooNew, version, domain.CollectionFormatVersion)
	}
	doc, err = config.ParseCollection(raw)
	if err != nil {
		return nil, false, nil, fmt.Errorf("%w: %v", ErrInvalidCollection, err)
	}
	return doc, container, warnings, nil
}

// errNotADocument is what a URL answered with when it is neither an lmm
// collection nor a profile export: a web page, most often.
const errNotADocument = "the address returned something that is not an lmm collection or a profile export (a web page, perhaps: link to the file itself)"

// liftProfileExport reads a version-0 profile export as a collection
// document of FormatVersion 0: its name, game id, mods, hooks, overrides
// and plugin list. A body that does not parse as one, or names no profile,
// is not one.
func liftProfileExport(raw []byte) (*domain.CollectionDocument, error) {
	profile, err := config.ImportProfile(raw)
	if err != nil || strings.TrimSpace(profile.Name) == "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCollection, errNotADocument)
	}
	doc := &domain.CollectionDocument{
		Name:          profile.Name,
		GameID:        profile.GameID,
		Hooks:         profile.Hooks,
		HooksExplicit: profile.HooksExplicit,
		Plugins:       profile.Plugins.Clone(),
		Items:         make([]domain.CollectionItem, len(profile.Mods)),
	}
	for i, ref := range profile.Mods {
		doc.Items[i] = domain.CollectionItem{Ref: ref}
	}
	if len(profile.Overrides) > 0 {
		doc.Overrides = make(map[string]string, len(profile.Overrides))
		for path, content := range profile.Overrides {
			doc.Overrides[path] = string(content)
		}
	}
	return doc, nil
}

// matchImportGame is matchCollectionGame, plus a version-0 export's own
// rule: it names its game only by the author's local id, so with no game
// asked for, a configured game of that id is the match ("game_id").
func (s *Service) matchImportGame(doc *domain.CollectionDocument, gameID string) (*domain.Game, string, error) {
	if doc.FormatVersion > 0 || gameID != "" {
		return s.matchCollectionGame(doc.Game, gameID)
	}
	if doc.GameID != "" {
		if game, err := s.GetGame(doc.GameID); err == nil {
			return game, "game_id", nil
		}
	}
	name := "the game " + doc.GameID
	if doc.GameID == "" {
		name = "an unnamed game"
	}
	return nil, "", &CollectionGameError{Game: domain.CollectionGame{Name: name}}
}

// redactedCollection is c as a plan shows it: its origin's import address
// redacted. c itself is left as it is.
func redactedCollection(c *domain.CollectionInfo) *domain.CollectionInfo {
	if c == nil || c.Origin == nil || c.Origin.ImportURL == "" {
		return c
	}
	out := *c
	out.Origin = c.Origin.Redacted()
	return &out
}
