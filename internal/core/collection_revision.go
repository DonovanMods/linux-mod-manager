package core

// collection_revision.go checks a collection profile's collection as the
// update unit (#673): one request to the collection's origin for its latest
// published revision, in place of checking the mods the collection locks
// one by one (#667 already leaves those out). A mod the user unlocked is no
// longer the collection's and is checked like any other.
//
// What each origin gets:
//
//   - a source's collection (NexusMods, `--nexus-collection`): the source's
//     source.CollectionRevisionChecker, one GraphQL request through its
//     retrying client (#671), so a rate-limit hold applies;
//   - an .lmmc address (#622): a conditional GET with the ETag and
//     Last-Modified the address answered the import (or the last update)
//     with (#674). A 304 is up to date; a 200 is read, within the import's
//     size cap, for its collection version. With no validators recorded (a
//     profile imported before #674, or a server sending neither) it is a
//     plain GET of the whole document - at most once per cache window;
//   - a file import: not checked - there is nothing to ask. A collection
//     the user AUTHORED (an exported profile: an identity, no origin) is
//     no import at all and reports nothing (#673 review): its refs carry
//     no from_collection, which is how the two are told apart;
//   - a Steam Workshop collection: its import records no collection
//     identity, so there is no collection to report; its items are
//     ordinary rows Steam updates.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// The origins a CollectionRevisionStatus reports.
const (
	// CollectionOriginSource: a source's collection (NexusMods).
	CollectionOriginSource = "source"
	// CollectionOriginURL: an .lmmc document imported from an https
	// address (#622).
	CollectionOriginURL = "url"
	// CollectionOriginFile: a collection imported from a file, or one
	// whose profile records no origin.
	CollectionOriginFile = "file"
)

// Why a CollectionRevisionStatus was not checked (Reason).
const (
	// CollectionUncheckedURL: the profile's .lmmc address is not one lmm
	// fetches (not an absolute https URL). Since #674 every other address
	// is checked, by a conditional GET.
	CollectionUncheckedURL = "url"
	// CollectionUncheckedFile: a file import has nowhere to ask.
	CollectionUncheckedFile = "file"
	// CollectionUncheckedUnsupported: the collection's source cannot
	// answer a revision check, or the game has no id on it.
	CollectionUncheckedUnsupported = "unsupported"
)

// CollectionRevisionStatus is a collection profile's collection, checked as
// the update unit (#673): UpdateCheckReport.CollectionRevision. Installed is
// the revision (version) the profile has; when Checked, Latest is the
// origin's latest published one and Available says it is newer. When not
// Checked, Reason says why it could not be asked (CollectionUnchecked*),
// or Error why the request failed - neither fails the update check, whose
// mods were checked all the same. Sync reports that `lmm profile sync
// <profile> --collection` (the web UI's "Update collection…") can update
// it.
type CollectionRevisionStatus struct {
	// Name is the collection's title, else the profile's name.
	Name string `json:"name"`
	// Origin is CollectionOriginSource, CollectionOriginURL or
	// CollectionOriginFile; SourceID names the source for the first.
	Origin    string `json:"origin"`
	SourceID  string `json:"source_id,omitempty"`
	Installed int    `json:"installed,omitzero"`
	Latest    int    `json:"latest,omitzero"`
	Available bool   `json:"available,omitzero"`
	// InstalledUnknown is set when the profile records no installed
	// revision: Latest is reported but not compared, so Available is
	// never set (#673 review). omitzero.
	InstalledUnknown bool `json:"installed_unknown,omitzero"`
	// LatestUnknown is set when the check answered but the collection it
	// found carries no version (#674 review: an .lmmc address serving a
	// document without one, or a 304 for a collection imported without
	// one): Checked, with nothing to compare, so Latest is 0 and
	// Available never set. omitzero.
	LatestUnknown bool   `json:"latest_unknown,omitzero"`
	Checked       bool   `json:"checked"`
	Reason        string `json:"reason,omitempty"`
	Error         string `json:"error,omitempty"`
	// HeldUntil and HeldBy are set when the source refused the request
	// because it is holding requests (#671's RetryTransport, a
	// *source.RetryLaterError): the service (HeldBy, e.g. "NexusMods") and
	// when it will be asked again, as data a frontend renders in the
	// reader's clock. Error carries the same in words. omitzero.
	HeldUntil time.Time `json:"held_until,omitzero"`
	HeldBy    string    `json:"held_by,omitempty"`
	Sync      bool      `json:"sync,omitzero"`
}

// collectionRevisionTTL is how long a latest-revision answer is kept
// (#673 review): long enough that opening pages does not re-ask, short
// enough that a revision published since shows within minutes.
const collectionRevisionTTL = 5 * time.Minute

// collectionRevisionCheckTimeout bounds an .lmmc address's revision check
// (#674 review): it runs inside `lmm update` and the web Updates card, so
// a slow address must not stall them for collectionFetchTimeout, the
// import's and the sync's own deadline. A var for the test.
var collectionRevisionCheckTimeout = 20 * time.Second

// collectionRevisionEntry is one kept answer: the latest revision, or -
// for a URL origin, whose failed answers are kept too (#674 review) - why
// it could not be had.
type collectionRevisionEntry struct {
	latest    int
	failure   string
	heldUntil time.Time
	heldBy    string
	at        time.Time
}

// collectionRevisionKey keys a kept answer by source, game and slug.
func collectionRevisionKey(sourceID, gameSourceID, slug string) string {
	return sourceID + "\x00" + gameSourceID + "\x00" + slug
}

// keptCollectionRevision is the latest revision kept for key, if it is
// fresh and was an answer rather than a failure.
func (s *Service) keptCollectionRevision(key string) (int, bool) {
	e, ok := s.keptCollectionEntry(key)
	if !ok || e.failure != "" {
		return 0, false
	}
	return e.latest, true
}

// keptCollectionEntry is the entry kept for key, if it is fresh.
func (s *Service) keptCollectionEntry(key string) (collectionRevisionEntry, bool) {
	s.collectionRevisionsMu.Lock()
	defer s.collectionRevisionsMu.Unlock()
	e, ok := s.collectionRevisions[key]
	if !ok || s.now().Sub(e.at) >= collectionRevisionTTL {
		return collectionRevisionEntry{}, false
	}
	return e, true
}

// keepCollectionRevision keeps a successful answer for key.
func (s *Service) keepCollectionRevision(key string, latest int) {
	s.keepCollectionEntry(key, collectionRevisionEntry{latest: latest})
}

// keepCollectionEntry keeps e for key, stamped now.
func (s *Service) keepCollectionEntry(key string, e collectionRevisionEntry) {
	s.collectionRevisionsMu.Lock()
	defer s.collectionRevisionsMu.Unlock()
	if s.collectionRevisions == nil {
		s.collectionRevisions = make(map[string]collectionRevisionEntry)
	}
	e.at = s.now()
	s.collectionRevisions[key] = e
}

// forgetCollectionRevision drops the kept answer for a collection - a
// collection update moved the profile to a new revision.
func (s *Service) forgetCollectionRevision(sourceID, gameSourceID, slug string) {
	s.collectionRevisionsMu.Lock()
	defer s.collectionRevisionsMu.Unlock()
	delete(s.collectionRevisions, collectionRevisionKey(sourceID, gameSourceID, slug))
}

// collectionRevision is game/profileName's collection status, or nil for a
// profile that is not a collection's import. It makes at most one request,
// and none while a kept answer is fresh, unless refresh.
func (s *Service) collectionRevision(ctx context.Context, game *domain.Game, profileName string, refresh bool) *CollectionRevisionStatus {
	prof, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil || prof.Collection == nil {
		return nil
	}
	c := prof.Collection
	st := &CollectionRevisionStatus{Name: firstNonEmpty(c.Title, prof.Name), Installed: c.Version}
	origin, originErr := updateOrigin(prof)
	st.Sync = originErr == nil
	switch {
	case originErr != nil && !slices.ContainsFunc(prof.Mods, func(r domain.ModReference) bool { return r.FromCollection }):
		// #673 review: no origin and no entry the collection brought in -
		// the user's own collection (an exported profile), not an import.
		return nil
	case originErr != nil:
		st.Origin, st.Reason = CollectionOriginFile, CollectionUncheckedFile
		return st
	case origin.input == CollectionFromURL:
		st.Origin = CollectionOriginURL
		s.checkURLCollection(ctx, c, origin.url, st, refresh)
		return st
	}
	st.Origin, st.SourceID = CollectionOriginSource, origin.sourceID
	if st.Installed == 0 {
		// A duplicate keeps the origin's revision but not the version
		// (duplicateCollection), as PlanCollectionUpdate reads it.
		st.Installed = c.Origin.Revision
	}
	src, err := s.GetSource(origin.sourceID)
	checker, ok := src.(source.CollectionRevisionChecker)
	gameSourceID := game.SourceIDs[origin.sourceID]
	if err != nil || !ok || gameSourceID == "" {
		st.Reason = CollectionUncheckedUnsupported
		return st
	}
	key := collectionRevisionKey(origin.sourceID, gameSourceID, origin.slug)
	latest, kept := 0, false
	if !refresh {
		latest, kept = s.keptCollectionRevision(key)
	}
	if !kept {
		latest, err = checker.LatestCollectionRevision(ctx, gameSourceID, origin.slug)
		if err != nil {
			st.Error = err.Error()
			var later *source.RetryLaterError
			if errors.As(err, &later) {
				st.HeldUntil, st.HeldBy = later.Until, later.Source
			}
			return st
		}
		s.keepCollectionRevision(key, latest)
	}
	settleRevision(st, latest)
	return st
}

// settleRevision records a checked answer, latest, on st: compared with the
// installed revision when there is one.
func settleRevision(st *CollectionRevisionStatus, latest int) {
	st.Checked, st.Latest = true, latest
	if st.Installed == 0 {
		st.InstalledUnknown = true
		return
	}
	st.Available = latest > st.Installed
}

// urlRevisionKey keys a URL origin's kept answer (#674) by its address.
func urlRevisionKey(importURL string) string {
	return collectionRevisionKey(CollectionOriginURL, "", importURL)
}

// checkURLCollection checks an .lmmc address's collection (#674) onto st:
// a conditional GET with c's recorded validators - a 304 is the installed
// version - or, for a 200, the version the document now carries. The
// validators are not updated: they stay the installed version's until a
// collection update records the new one's, so a newer version is reported
// until it is taken. A document that is another collection, or an older
// version than the installed one, is an error, as the update would refuse
// it; one with no version is checked with its version unknown.
//
// The request has its own short deadline (collectionRevisionCheckTimeout),
// and every outcome - a failure too - is kept for the cache window, so an
// address that is gone or slow is not asked on every page open; refresh
// asks again.
func (s *Service) checkURLCollection(ctx context.Context, c *domain.CollectionInfo, importURL string, st *CollectionRevisionStatus, refresh bool) {
	key := urlRevisionKey(importURL)
	if !refresh {
		if e, ok := s.keptCollectionEntry(key); ok {
			applyURLOutcome(st, e)
			return
		}
	}
	u, err := parseCollectionURL(importURL)
	if err != nil {
		// Not an address lmm fetches: said as the import would say it.
		st.Reason, st.Error = CollectionUncheckedURL, err.Error()
		return
	}
	e := s.askURLCollection(ctx, c, u, st.Installed)
	s.keepCollectionEntry(key, e)
	applyURLOutcome(st, e)
}

// askURLCollection is checkURLCollection's request and its reading.
func (s *Service) askURLCollection(ctx context.Context, c *domain.CollectionInfo, u *url.URL, installed int) collectionRevisionEntry {
	ctx, cancel := context.WithTimeout(ctx, collectionRevisionCheckTimeout)
	defer cancel()
	fetched, err := s.fetchCollection(ctx, u, collectionValidators{etag: c.Origin.ETag, lastModified: c.Origin.LastModified})
	if err != nil {
		e := collectionRevisionEntry{failure: err.Error()}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
			e.failure = fmt.Sprintf("%s did not answer within %s", domain.RedactURL(u.String()), collectionRevisionCheckTimeout)
		}
		var later *source.RetryLaterError
		if errors.As(err, &later) {
			e.heldUntil, e.heldBy = later.Until, later.Source
		}
		return e
	}
	if fetched.notModified {
		return collectionRevisionEntry{latest: installed}
	}
	doc, _, _, _, err := s.readCollectionData(fetched.data, true)
	if err != nil {
		return collectionRevisionEntry{failure: err.Error()}
	}
	latest := doc.Info.Version
	switch {
	case c.ID != "" && doc.Info.ID != c.ID:
		return collectionRevisionEntry{failure: (&CollectionUpdateMismatchError{Code: CollectionUpdateDifferentCollection, ID: c.ID, FetchedID: doc.Info.ID}).Error()}
	case latest > 0 && installed > 0 && latest < installed:
		// As PlanCollectionUpdate refuses it.
		return collectionRevisionEntry{failure: (&CollectionUpdateMismatchError{Code: CollectionUpdateOlderVersion, Version: installed, FetchedVersion: latest}).Error()}
	}
	return collectionRevisionEntry{latest: latest}
}

// applyURLOutcome puts a URL check's outcome, e, on st: its failure, or
// the version it found - unknown when the collection carries none.
func applyURLOutcome(st *CollectionRevisionStatus, e collectionRevisionEntry) {
	if e.failure != "" {
		st.Error, st.HeldUntil, st.HeldBy = e.failure, e.heldUntil, e.heldBy
		return
	}
	if e.latest == 0 {
		st.Checked, st.LatestUnknown = true, true
		return
	}
	settleRevision(st, e.latest)
}

// forgetURLCollectionRevision drops a URL origin's kept answer (#674).
func (s *Service) forgetURLCollectionRevision(importURL string) {
	s.collectionRevisionsMu.Lock()
	defer s.collectionRevisionsMu.Unlock()
	delete(s.collectionRevisions, urlRevisionKey(importURL))
}
