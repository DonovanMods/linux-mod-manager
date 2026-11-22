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
//   - an .lmmc address (#622): not checked. lmm records no validator (ETag,
//     Last-Modified) for the address, so the only way to learn its version
//     is to fetch the whole document - which is what the sync plan does;
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
	// CollectionUncheckedURL: an .lmmc address can only be checked by
	// fetching the whole document, which is the sync plan's job.
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
	InstalledUnknown bool   `json:"installed_unknown,omitzero"`
	Checked          bool   `json:"checked"`
	Reason           string `json:"reason,omitempty"`
	Error            string `json:"error,omitempty"`
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

// collectionRevisionEntry is one kept answer.
type collectionRevisionEntry struct {
	latest int
	at     time.Time
}

// collectionRevisionKey keys a kept answer by source, game and slug.
func collectionRevisionKey(sourceID, gameSourceID, slug string) string {
	return sourceID + "\x00" + gameSourceID + "\x00" + slug
}

// keptCollectionRevision is the answer kept for key, if it is fresh.
func (s *Service) keptCollectionRevision(key string) (int, bool) {
	s.collectionRevisionsMu.Lock()
	defer s.collectionRevisionsMu.Unlock()
	e, ok := s.collectionRevisions[key]
	if !ok || s.now().Sub(e.at) >= collectionRevisionTTL {
		return 0, false
	}
	return e.latest, true
}

// keepCollectionRevision keeps a successful answer for key.
func (s *Service) keepCollectionRevision(key string, latest int) {
	s.collectionRevisionsMu.Lock()
	defer s.collectionRevisionsMu.Unlock()
	if s.collectionRevisions == nil {
		s.collectionRevisions = make(map[string]collectionRevisionEntry)
	}
	s.collectionRevisions[key] = collectionRevisionEntry{latest: latest, at: s.now()}
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
		st.Origin, st.Reason = CollectionOriginURL, CollectionUncheckedURL
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
	st.Checked, st.Latest = true, latest
	if st.Installed == 0 {
		st.InstalledUnknown = true
		return st
	}
	st.Available = latest > st.Installed
	return st
}
