// collection_update.go updates a collection profile from its origin (#613
// C8; design docs/plans/2026-11-10-collections-design.md §6 "Revision
// updates" and §12 "Updates come from origin"): `lmm profile sync
// --collection` and the web UI's "Update collection".
//
// Where the newer collection comes from is the profile's own record:
//
//   - a profile imported from an https address (origin.import_url, #622)
//     fetches that address again, through the URL import's fetch (https
//     only, size-capped, redirects checked), and compares the document's
//     id - another collection is refused - and its version: a newer one is
//     an update, the same one is nothing to do;
//   - a profile whose origin is a source's collection (origin.source_id and
//     slug: a NexusMods collection, #607) resolves the collection's newest
//     revision and converts it through the import's own cached path.
//
// Either way the result is a version-1 collection document, read and
// sanitised exactly as an import reads one, so every L1 gate - the
// feature gate, hooks, overrides, loader configurations - applies to the
// new document as it would to a fresh import of it.
//
// The plan diffs the profile against the new document, entry by entry:
//
//   - an entry the collection brought in (from_collection) follows the new
//     pin - version, files, lock, installer choices, mod type - through a
//     sync-only bypass of its lock: the lock is the collection's;
//   - an entry the user locked or edited by hand (no marker) that the new
//     document changes is a conflict: the user's version wins unless the
//     caller overrides it (CollectionUpdateOptions.Override);
//   - an entry the old version brought in that the new one drops is an
//     obsolete member, offered for removal and never removed silently
//     (CollectionUpdateOptions.Remove); kept, it becomes the user's;
//   - an entry the user added is untouched.
//
// Apply builds the profile the update leads to and converges to it through
// the import's two-phase engine (acquire every download first, then
// commit), so a refusal - an installer question, a source collection's
// failed download - writes nothing. Obsolete members the caller chose are
// then uninstalled, and a source collection's old revision's recipe cache
// entry is removed once no profile names it.
package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// CollectionUpdateRequest is what PlanCollectionUpdate plans.
type CollectionUpdateRequest struct {
	// Profile names the collection profile to update.
	Profile string
	// WithOptional opts every optional item of a source collection in, and
	// Optional the ones it names (ConvertedCollectionItem.ID), as an import
	// does. An optional item the profile already has stays in without
	// either. Both apply to a source collection only.
	WithOptional bool
	Optional     []string
}

// ErrCollectionNotUpdatable refuses an update of a profile that records no
// origin to update from: it was not imported from a collection, or it was
// imported from a local file (whose newer version the user imports the
// same way, with --force).
var ErrCollectionNotUpdatable = errors.New("this profile has no collection origin to update from")

// IsCollectionNotUpdatable reports whether err refused an update for a
// profile with no origin (ErrCollectionNotUpdatable).
func IsCollectionNotUpdatable(err error) bool { return errors.Is(err, ErrCollectionNotUpdatable) }

// Codes of a CollectionUpdateMismatchError.
const (
	// CollectionUpdateDifferentCollection: the origin now serves another
	// collection (a different id).
	CollectionUpdateDifferentCollection = "different_collection"
	// CollectionUpdateOlderVersion: the origin now serves an older version
	// than the profile has.
	CollectionUpdateOlderVersion = "older_version"
)

// CollectionUpdateMismatchError refuses an update whose origin no longer
// serves a newer version of the profile's collection: another collection,
// or an older version of it. Nothing is written.
type CollectionUpdateMismatchError struct {
	// Code is CollectionUpdateDifferentCollection or
	// CollectionUpdateOlderVersion.
	Code string `json:"code"`
	// ID and Version are what the profile records; FetchedID and
	// FetchedVersion what the origin served.
	ID             string `json:"id,omitempty"`
	Version        int    `json:"version,omitzero"`
	FetchedID      string `json:"fetched_id,omitempty"`
	FetchedVersion int    `json:"fetched_version,omitzero"`
}

// Error implements error.
func (e *CollectionUpdateMismatchError) Error() string {
	if e.Code == CollectionUpdateOlderVersion {
		return fmt.Sprintf("the collection's origin now serves version %d, older than the profile's version %d; nothing to update to", e.FetchedVersion, e.Version)
	}
	fetched := e.FetchedID
	if fetched == "" {
		fetched = "a document with no id"
	} else {
		fetched = fmt.Sprintf("collection %q", fetched)
	}
	return fmt.Sprintf("the collection's origin now serves %s, not this profile's collection %q; refusing to update from it (import it as a new profile instead)", fetched, e.ID)
}

// Details implements the --json error envelope's extension point.
func (e *CollectionUpdateMismatchError) Details() any { return e }

// IsCollectionUpdateMismatch reports whether err is an update refused for
// what its origin now serves.
func IsCollectionUpdateMismatch(err error) bool {
	var target *CollectionUpdateMismatchError
	return errors.As(err, &target)
}

// CollectionUpdateItem is one entry of a collection update plan.
type CollectionUpdateItem struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the collection's display name for the mod, when it has one.
	Name string `json:"name,omitempty"`
	// Version is the profile's version of the entry, and NewVersion the new
	// collection's: one or the other is empty for an added or removed
	// entry.
	Version    string `json:"version,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
	// Changes names what the new collection changes about a changed or
	// conflicting entry: "version", "files", "lock", "installer_choices",
	// "mod_type".
	Changes []string `json:"changes,omitempty"`
	// Locked is whether the profile's entry is locked: for a conflict, the
	// user's lock that wins by default.
	Locked bool `json:"locked,omitzero"`
	// DisplayVersion and NewDisplayVersion are what a surface shows in
	// Version's and NewVersion's place when they are not versions a person
	// reads (a Steam Workshop item's content id, #458): set for such an
	// entry only.
	DisplayVersion    string `json:"display_version,omitempty"`
	NewDisplayVersion string `json:"new_display_version,omitempty"`
	// Note is what a person should know about the change: today, that an
	// entry's files change at the same version, which is recorded on the
	// profile and applies when the mod is next reinstalled (#639).
	Note string `json:"note,omitempty"`
}

// sameVersionFilesNote is CollectionUpdateItem.Note for a pin whose files
// change and whose version does not.
const sameVersionFilesNote = "its files change at the same version: the profile records them, and they apply when the mod is next reinstalled"

// noteChanges sets the entry's Note from its Changes.
func (it *CollectionUpdateItem) noteChanges() {
	if slices.Contains(it.Changes, "files") && !slices.Contains(it.Changes, "version") {
		it.Note = sameVersionFilesNote
	}
}

// Key is the entry's domain.ModKey, which CollectionUpdateOptions names.
func (i CollectionUpdateItem) Key() string { return domain.ModKey(i.SourceID, i.ModID) }

// CollectionPluginChange is what an update does to the profile's plugin
// list: the plugins the new list adds and drops, whether it reorders the
// plugins both have or changes the rules, and the new list's summary.
type CollectionPluginChange struct {
	Added     []string                 `json:"added,omitempty"`
	Removed   []string                 `json:"removed,omitempty"`
	Reordered bool                     `json:"reordered,omitzero"`
	Rules     bool                     `json:"rules_changed,omitzero"`
	Summary   *CollectionPluginSummary `json:"summary,omitempty"`
}

// CollectionUpdatePlan is PlanCollectionUpdate's preview: the profile's
// collection, the version it moves from and to, every entry the update
// adds, changes, offers for removal or conflicts with, and everything about
// the new document an import's plan would show - its gate's verdict, its
// hooks (never applied), its overrides, its plugin list and its external
// items. Nothing is written to plan it.
type CollectionUpdatePlan struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Origin is where the update came from: "url" (the address the
	// profile was imported from) or "source" (a source's collection).
	Origin CollectionInput `json:"origin"`
	// URL is the address fetched, redacted (domain.RedactURL), for a URL
	// origin; SourceID and Slug name a source origin's collection.
	URL      string `json:"url,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	Slug     string `json:"slug,omitempty"`
	// ID is the collection's id, and Name its display name.
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Version is the profile's version of the collection (a source
	// collection's revision), and NewVersion the origin's.
	Version    int `json:"version,omitzero"`
	NewVersion int `json:"new_version,omitzero"`
	// UpToDate is true when the origin serves the version the profile has:
	// there is nothing to do, and the buckets below are empty.
	UpToDate bool `json:"up_to_date"`
	// NoChanges is true when the new version changes none of the profile's
	// entries, plugins, overrides or external items (its description may
	// still have changed): Apply records the new version and installs
	// nothing.
	NoChanges bool `json:"no_changes"`

	// Added are the new version's entries the profile lacks: installed by
	// the update. Changed are the collection's entries whose pin the new
	// version moves: reinstalled at it, locked or not.
	Added   []CollectionUpdateItem `json:"added,omitempty"`
	Changed []CollectionUpdateItem `json:"changed,omitempty"`
	// Removed are the obsolete members: entries the collection brought in
	// that the new version drops. Each is kept, as the user's, unless the
	// apply names it for removal.
	Removed []CollectionUpdateItem `json:"removed,omitempty"`
	// Conflicts are the entries the user locked or edited that the new
	// version changes: the user's version is kept unless the apply
	// overrides it.
	Conflicts []CollectionUpdateItem `json:"conflicts,omitempty"`

	// Plugins is what the update does to the profile's plugin list; absent
	// when nothing.
	Plugins *CollectionPluginChange `json:"plugins,omitempty"`
	// OverridesAdded and OverridesChanged are the config overrides the new
	// version adds or rewrites; RestrictedOverrides those of them that are
	// not text config files (or are loader configuration), which the apply
	// skips unless AllowAnyOverrides. An override the new version no longer
	// carries is kept: it may be the user's.
	OverridesAdded      []string             `json:"overrides_added,omitempty"`
	OverridesChanged    []string             `json:"overrides_changed,omitempty"`
	RestrictedOverrides []RestrictedOverride `json:"restricted_overrides,omitempty"`
	// ExternalAdded and ExternalRemoved are the items to get by hand the
	// new version adds and drops.
	ExternalAdded   []domain.CollectionExternalItem `json:"external_added,omitempty"`
	ExternalRemoved []domain.CollectionExternalItem `json:"external_removed,omitempty"`
	// Hooks lists every hook the new document carries. An update never
	// applies a document's hooks (it came from the network); the profile
	// keeps its own.
	Hooks []ImportHook `json:"hooks,omitempty"`
	// InstallInstructions are the new version's.
	InstallInstructions string `json:"install_instructions,omitempty"`
	// Requires and Unsupported are the gate's verdict on the new document,
	// as an import's: a non-empty Unsupported means Apply refuses.
	Requires    []string                       `json:"requires,omitempty"`
	Unsupported []UnsupportedCollectionFeature `json:"unsupported,omitempty"`
	// Warnings are what the reader skipped or could not check, and what
	// the update could not decide for the user.
	Warnings []string `json:"warnings,omitempty"`
	// Source is a source collection's conversion (#607): every item with
	// its state, the curator's tools and notes. Absent for a URL origin.
	Source *ConvertedCollection `json:"source,omitempty"`
	// RecordedOnly and ActiveProfile say the profile is not the game's
	// active one: the update records and downloads, and deploys nothing.
	RecordedOnly  bool   `json:"recorded_only,omitzero"`
	ActiveProfile string `json:"active_profile,omitempty"`

	// base is the profile as planned: Apply refuses (ErrStalePlan) when
	// it changed since.
	base *domain.Profile
	// doc is the new document, read and sanitised.
	doc *domain.CollectionDocument
	// info is the collection record the profile takes: the new document's,
	// with the profile's own import address.
	info *domain.CollectionInfo
	// autoUpdate names the source collection's "latest" entries.
	autoUpdate map[string]bool
	// manualDownload: the account is served no downloads (a free NexusMods
	// account): added and changed entries are recorded pending.
	manualDownload bool
	snapshot       installedSnapshot
}

// CollectionUpdateOptions is the user's say over an update, taken at Apply
// so a frontend can show the plan and then ask.
type CollectionUpdateOptions struct {
	// Remove names obsolete members (CollectionUpdatePlan.Removed, by
	// domain.ModKey) to remove and uninstall; RemoveObsolete removes them
	// all. The rest are kept, as the user's.
	Remove         []string
	RemoveObsolete bool
	// Override names conflicts (CollectionUpdatePlan.Conflicts) to resolve
	// the collection's way: the entry takes the new pin and follows the
	// collection again. OverrideConflicts overrides them all. The rest keep
	// the user's version.
	Override          []string
	OverrideConflicts bool
	// Fomod answers installer questions, as an import's Options.Fomod.
	Fomod map[string]domain.FomodChoices
	// AllowAnyOverrides saves the new version's restricted overrides too,
	// as an import's --allow-any-overrides.
	AllowAnyOverrides bool
}

// CollectionUpdateResult is what ApplyCollectionUpdate did.
type CollectionUpdateResult struct {
	Profile string `json:"profile"`
	// UpToDate echoes the plan: nothing was done.
	UpToDate bool `json:"up_to_date"`
	// Version and NewVersion are the versions the profile moved between.
	Version    int `json:"version,omitzero"`
	NewVersion int `json:"new_version,omitzero"`
	// Added, Changed and Overridden count the entries the update added,
	// moved to the new pin, and took the collection's way over the user's.
	// Kept counts the obsolete members kept, and Conflicts the conflicts
	// left the user's way.
	Added      int `json:"added"`
	Changed    int `json:"changed"`
	Overridden int `json:"overridden"`
	Kept       int `json:"kept"`
	Conflicts  int `json:"conflicts"`
	// Removed names the obsolete members removed (domain.ModKey).
	Removed []string `json:"removed,omitempty"`
	// Import is the converge's own result: what was installed, failed or
	// held back.
	Import *ProfileImportResult `json:"import,omitempty"`
	// Warnings are what the removals and the cache clean-up could not do.
	Warnings []string `json:"warnings,omitempty"`
}

// collectionOrigin is where a profile's collection updates from.
type collectionOrigin struct {
	input    CollectionInput
	url      string
	sourceID string
	slug     string
}

// updateOrigin is profile's origin to update from, or
// ErrCollectionNotUpdatable. A recorded import address wins: it is the
// subscription the user chose, and a document fetched from one can also
// carry a source's credit.
func updateOrigin(profile *domain.Profile) (collectionOrigin, error) {
	c := profile.Collection
	if c == nil || c.Origin == nil {
		return collectionOrigin{}, fmt.Errorf("%w: profile %s was not imported from a collection address or a source's collection (one imported from a file updates by importing its new version with --force)", ErrCollectionNotUpdatable, profile.Name)
	}
	o := c.Origin
	switch {
	case o.ImportURL != "":
		return collectionOrigin{input: CollectionFromURL, url: o.ImportURL}, nil
	case o.SourceID != "" && o.Slug != "":
		return collectionOrigin{input: CollectionFromSource, sourceID: o.SourceID, slug: o.Slug}, nil
	}
	return collectionOrigin{}, fmt.Errorf("%w: profile %s's collection records no address or source collection", ErrCollectionNotUpdatable, profile.Name)
}

// PlanCollectionUpdate fetches the newer version of game/req.Profile's
// collection from the profile's origin and plans updating the profile to
// it. Nothing is written but a source collection's recipe cache entry. A
// profile with no origin is ErrCollectionNotUpdatable; an origin serving
// another collection, or an older version, is
// *CollectionUpdateMismatchError; one serving the profile's own version
// plans UpToDate.
func (s *Service) PlanCollectionUpdate(ctx context.Context, game *domain.Game, req CollectionUpdateRequest) (*CollectionUpdatePlan, error) {
	if err := s.refuseModPathMoved(ctx, game.ID); err != nil {
		return nil, err
	}
	pm := s.NewProfileManager()
	base, err := pm.Get(ctx, game.ID, req.Profile)
	if err != nil {
		return nil, err
	}
	origin, err := updateOrigin(base)
	if err != nil {
		return nil, err
	}
	if origin.input != CollectionFromSource && (req.WithOptional || len(req.Optional) > 0) {
		return nil, errors.New("opting optional items in applies to a source's collection (NexusMods) only")
	}
	installed, err := s.GetInstalledMods(ctx, game.ID, req.Profile)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	snapshot, err := s.snapshotOf(game.ID, installed)
	if err != nil {
		return nil, err
	}

	plan := &CollectionUpdatePlan{
		GameID: game.ID, Profile: base.Name, Origin: origin.input,
		ID: base.Collection.ID, Version: base.Collection.Version,
		Name:     firstNonEmpty(base.Collection.Title, base.Name),
		base:     base,
		snapshot: snapshot,
	}
	if plan.Version == 0 && origin.input == CollectionFromSource {
		// A duplicate keeps the origin's revision but not the version
		// (duplicateCollection): the revision is what it has.
		plan.Version = base.Collection.Origin.Revision
	}
	var warnings []string
	var conv *ConvertedCollection
	var doc *domain.CollectionDocument
	switch origin.input {
	case CollectionFromURL:
		plan.URL = domain.RedactURL(origin.url)
		u, err := parseCollectionURL(origin.url)
		if err != nil {
			return nil, err
		}
		data, err := s.fetchCollectionDocument(ctx, u)
		if err != nil {
			return nil, err
		}
		var more []string
		doc, _, more, err = s.readCollectionData(data, true)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, more...)
	default:
		plan.SourceID, plan.Slug = origin.sourceID, origin.slug
		conv, err = s.convertForUpdate(ctx, game, base, origin, req)
		if err != nil {
			return nil, err
		}
		data, err := config.MarshalCollection(conv.Document)
		if err != nil {
			return nil, fmt.Errorf("writing the converted collection: %w", err)
		}
		var more []string
		doc, _, more, err = s.readCollectionData(data, false)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, more...)
	}
	more, err := sanitizeCollection(doc)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, more...)

	// Identity: the same collection, and not an older version of it.
	fetchedID, fetchedVersion := doc.Info.ID, doc.Info.Version
	if plan.ID != "" && fetchedID != plan.ID {
		return nil, &CollectionUpdateMismatchError{Code: CollectionUpdateDifferentCollection, ID: plan.ID, Version: plan.Version, FetchedID: fetchedID, FetchedVersion: fetchedVersion}
	}
	if plan.ID == "" && fetchedID != "" {
		warnings = append(warnings, fmt.Sprintf("the profile records no collection id, and its origin now serves collection %q: updating to it", fetchedID))
	}
	plan.NewVersion = fetchedVersion
	if plan.Name == base.Name {
		plan.Name = firstNonEmpty(doc.Info.Title, base.Name)
	}
	if fetchedVersion > 0 && plan.Version > 0 {
		switch {
		case fetchedVersion < plan.Version:
			return nil, &CollectionUpdateMismatchError{Code: CollectionUpdateOlderVersion, ID: plan.ID, Version: plan.Version, FetchedID: fetchedID, FetchedVersion: fetchedVersion}
		case fetchedVersion == plan.Version:
			plan.UpToDate, plan.NoChanges = true, true
			plan.Warnings = warnings
			return plan, nil
		}
	}

	info := doc.Info
	if origin.input == CollectionFromURL {
		// The address is this machine's subscription, kept as recorded.
		o := domain.CollectionOrigin{}
		if info.Origin != nil {
			o = *info.Origin
		}
		o.ImportURL = origin.url
		info.Origin = &o
	}
	plan.doc, plan.info = doc, &info
	if conv != nil {
		plan.Source = conv
		if len(conv.AutoUpdate) > 0 {
			plan.autoUpdate = make(map[string]bool, len(conv.AutoUpdate))
			for _, key := range conv.AutoUpdate {
				plan.autoUpdate[key] = true
			}
		}
		if tier, ok := s.accountTier(ctx, origin.sourceID); ok {
			conv.AccountTier = string(tier)
			conv.ManualDownload = tier == source.TierFree
			plan.manualDownload = conv.ManualDownload
		}
	}
	plan.InstallInstructions = info.InstallInstructions
	plan.Hooks = listHooks(doc.Hooks, doc.HooksExplicit)
	plan.Requires = unionFeatures(doc.Requires, deriveCollectionRequires(doc))
	plan.Unsupported = unsupportedFeatures(doc.Requires, deriveCollectionRequires(doc), s.modTypeRoutes(game))
	if conv != nil {
		plan.Unsupported = mergeUnsupported(plan.Unsupported, conv.Unsupported)
	}
	if len(plan.Hooks) > 0 {
		warnings = append(warnings, "the new version carries hooks; an update never applies a collection's hooks, and the profile keeps its own")
	}

	plan.diffEntries()
	plan.stampDisplay(s)
	if len(plan.Conflicts) > 0 && !slices.ContainsFunc(base.Mods, func(r domain.ModReference) bool { return r.FromCollection }) {
		warnings = append(warnings, "no entry of this profile is marked as the collection's (it was imported by an lmm before collection updates, or every entry was locked or unlocked by hand), so every entry the new version changes is listed as a conflict: override them to take the collection's version")
	}
	plan.diffRest(s, game)
	if w := pluginRuleCycle(doc.Plugins); w != "" {
		warnings = append(warnings, w)
	}
	plan.Warnings = warnings
	plan.NoChanges = len(plan.Added) == 0 && len(plan.Changed) == 0 && len(plan.Removed) == 0 && len(plan.Conflicts) == 0 &&
		plan.Plugins == nil && len(plan.OverridesAdded) == 0 && len(plan.OverridesChanged) == 0 &&
		len(plan.ExternalAdded) == 0 && len(plan.ExternalRemoved) == 0

	live, recordedOnly, err := s.importScope(ctx, game.ID, base.Name)
	if err != nil {
		return nil, err
	}
	plan.RecordedOnly, plan.ActiveProfile = recordedOnly, live
	return plan, nil
}

// convertForUpdate converts origin's source collection at its newest
// revision. An optional item the profile already has is opted in, so an
// update never reads it as dropped: it is converted once, and again with
// those items named, when there are any.
func (s *Service) convertForUpdate(ctx context.Context, game *domain.Game, base *domain.Profile, origin collectionOrigin, req CollectionUpdateRequest) (*ConvertedCollection, error) {
	opts := CollectionConvertOptions{WithOptional: req.WithOptional, Optional: slices.Clone(req.Optional)}
	conv, err := s.ConvertSourceCollection(ctx, game, origin.sourceID, origin.slug, opts)
	if err != nil || opts.WithOptional {
		return conv, err
	}
	have := firstRefs(base.Mods)
	var more []string
	for _, it := range conv.Items {
		if it.State == CollectionItemOptionalSkipped && it.ModID != "" && !slices.Contains(opts.Optional, it.ID) {
			if _, ok := have[domain.ModKey(origin.sourceID, it.ModID)]; ok {
				more = append(more, it.ID)
			}
		}
	}
	if len(more) == 0 {
		return conv, nil
	}
	opts.Optional = append(opts.Optional, more...)
	return s.ConvertSourceCollection(ctx, game, origin.sourceID, origin.slug, opts)
}

// pinChanges names what differs between the profile's entry have and the
// new collection's want: "version", "files", "lock" (when withLock),
// "installer_choices", "mod_type".
func pinChanges(have, want domain.ModReference, withLock bool) []string {
	var out []string
	if have.Version != want.Version {
		out = append(out, "version")
	}
	if !sameFileIDs(have.FileIDs, want.FileIDs) {
		out = append(out, "files")
	}
	if withLock && have.Locked != want.Locked {
		out = append(out, "lock")
	}
	if !have.FomodRecord().Equal(want.FomodRecord()) {
		out = append(out, "installer_choices")
	}
	if have.ModType != want.ModType {
		out = append(out, "mod_type")
	}
	return out
}

// sameFileIDs reports whether a and b name the same files, in any order.
func sameFileIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(x, y)
}

// diffEntries fills the four entry buckets from p.base and p.doc.
func (p *CollectionUpdatePlan) diffEntries() {
	have := firstRefs(p.base.Mods)
	inDoc := map[string]bool{}
	for _, item := range p.doc.Items {
		key := domain.ModKey(item.Ref.SourceID, item.Ref.ModID)
		if inDoc[key] {
			continue
		}
		inDoc[key] = true
		want := item.Ref
		entry := CollectionUpdateItem{SourceID: want.SourceID, ModID: want.ModID, Name: item.Name, NewVersion: want.Version}
		ref, ok := have[key]
		if !ok {
			p.Added = append(p.Added, entry)
			continue
		}
		entry.Version, entry.Locked = ref.Version, ref.Locked
		if ref.FromCollection {
			if entry.Changes = pinChanges(ref, want, true); len(entry.Changes) > 0 {
				entry.noteChanges()
				p.Changed = append(p.Changed, entry)
			}
			continue
		}
		if entry.Changes = pinChanges(ref, want, false); len(entry.Changes) > 0 {
			entry.noteChanges()
			p.Conflicts = append(p.Conflicts, entry)
		}
	}
	seen := map[string]bool{}
	for _, ref := range p.base.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] || inDoc[key] || !ref.FromCollection {
			seen[key] = true
			continue
		}
		seen[key] = true
		p.Removed = append(p.Removed, CollectionUpdateItem{SourceID: ref.SourceID, ModID: ref.ModID, Version: ref.Version, Locked: ref.Locked})
	}
}

// stampDisplay sets the display versions of every entry whose source
// versions by content id (a Steam Workshop item, #458): the revision date
// the profile's installed row has, or "-".
func (p *CollectionUpdatePlan) stampDisplay(s *Service) {
	for _, bucket := range [][]CollectionUpdateItem{p.Added, p.Changed, p.Removed, p.Conflicts} {
		for i := range bucket {
			it := &bucket[i]
			if !s.sourceIsWorkshop(it.SourceID) {
				continue
			}
			if it.Version != "" {
				it.DisplayVersion = NoRevisionDate
			}
			if it.NewVersion != "" {
				it.NewDisplayVersion = NoRevisionDate
			}
		}
	}
}

// diffRest fills the plan's plugin, override and external-item changes.
func (p *CollectionUpdatePlan) diffRest(s *Service, game *domain.Game) {
	if !p.doc.Plugins.IsZero() && !p.doc.Plugins.Equal(p.base.Plugins) {
		change := &CollectionPluginChange{}
		had, has := map[string]bool{}, map[string]bool{}
		for _, e := range p.base.Plugins.Order {
			had[pluginKey(e.Name)] = true
		}
		for _, e := range p.doc.Plugins.Order {
			has[pluginKey(e.Name)] = true
			if !had[pluginKey(e.Name)] {
				change.Added = append(change.Added, e.Name)
			}
		}
		var oldOrder, newOrder []string
		for _, e := range p.base.Plugins.Order {
			if !has[pluginKey(e.Name)] {
				change.Removed = append(change.Removed, e.Name)
			} else {
				oldOrder = append(oldOrder, pluginKey(e.Name)+fmt.Sprint(e.Enabled))
			}
		}
		for _, e := range p.doc.Plugins.Order {
			if had[pluginKey(e.Name)] {
				newOrder = append(newOrder, pluginKey(e.Name)+fmt.Sprint(e.Enabled))
			}
		}
		change.Reordered = !slices.Equal(oldOrder, newOrder)
		change.Rules = !reflect.DeepEqual(p.base.Plugins.Rules, p.doc.Plugins.Rules)
		_, managed := s.pluginRules(game)
		change.Summary = summarisePlugins(p.doc.Plugins, managed)
		p.Plugins = change
	}

	var touched []string
	for path, content := range p.doc.Overrides {
		old, ok := p.base.Overrides[path]
		switch {
		case !ok:
			p.OverridesAdded = append(p.OverridesAdded, path)
		case string(old) != content:
			p.OverridesChanged = append(p.OverridesChanged, path)
		default:
			continue
		}
		touched = append(touched, path)
	}
	sort.Strings(p.OverridesAdded)
	sort.Strings(p.OverridesChanged)
	if len(touched) > 0 {
		gate := &domain.Profile{Overrides: map[string][]byte{}}
		for _, path := range touched {
			gate.Overrides[path] = []byte(p.doc.Overrides[path])
		}
		p.RestrictedOverrides = restrictedOverrides(gate)
	}

	same := func(a, b domain.CollectionExternalItem) bool { return a.Name == b.Name && a.URL == b.URL }
	for _, e := range p.doc.External {
		if !slices.ContainsFunc(p.base.External, func(o domain.CollectionExternalItem) bool { return same(o, e) }) {
			p.ExternalAdded = append(p.ExternalAdded, e)
		}
	}
	for _, e := range p.base.External {
		if !slices.ContainsFunc(p.doc.External, func(o domain.CollectionExternalItem) bool { return same(o, e) }) {
			p.ExternalRemoved = append(p.ExternalRemoved, e)
		}
	}
}

// updateChoices is what the caller decided, resolved against the plan.
type updateChoices struct {
	remove   map[string]bool
	override map[string]bool
	// allowAny saves the new version's restricted overrides too.
	allowAny bool
}

// resolveUpdateChoices checks opts against plan: every key it names must
// be one of the plan's obsolete members or conflicts.
func resolveUpdateChoices(plan *CollectionUpdatePlan, opts CollectionUpdateOptions) (updateChoices, error) {
	c := updateChoices{remove: map[string]bool{}, override: map[string]bool{}, allowAny: opts.AllowAnyOverrides}
	pick := func(what string, all bool, named []string, items []CollectionUpdateItem, into map[string]bool) error {
		keys := map[string]bool{}
		for _, it := range items {
			keys[it.Key()] = true
		}
		for _, key := range named {
			if !keys[key] {
				return fmt.Errorf("%w: %s is not one of this update's %s", ErrBadUpdateChoice, key, what)
			}
			into[key] = true
		}
		if all {
			maps.Copy(into, keys)
		}
		return nil
	}
	if err := pick("obsolete members", opts.RemoveObsolete, opts.Remove, plan.Removed, c.remove); err != nil {
		return c, err
	}
	if err := pick("conflicts", opts.OverrideConflicts, opts.Override, plan.Conflicts, c.override); err != nil {
		return c, err
	}
	return c, nil
}

// ErrBadUpdateChoice refuses an update option naming an entry the plan
// does not offer the choice for: the caller's input.
var ErrBadUpdateChoice = errors.New("invalid collection update choice")

// updateTarget is the profile plan leads to under c, and the entries it
// marks as the collection's. The collection's entries come in the new
// version's order; every other entry the profile keeps (the user's own, a
// kept obsolete member, a duplicate) stays just after the collection entry
// it followed, so a mod the user placed keeps its place among them.
func updateTarget(plan *CollectionUpdatePlan, c updateChoices) (*domain.Profile, map[string]bool) {
	base := plan.base
	target := *base
	have := firstRefs(base.Mods)
	marks := map[string]bool{}
	inDoc := map[string]bool{}
	var entries []domain.ModReference
	for _, item := range plan.doc.Items {
		key := domain.ModKey(item.Ref.SourceID, item.Ref.ModID)
		if inDoc[key] {
			continue
		}
		inDoc[key] = true
		ref, ok := have[key]
		if ok && !ref.FromCollection && !c.override[key] {
			// The user's: kept as it is.
			entries = append(entries, ref)
			continue
		}
		next := item.Ref
		if ok {
			// The profile's own intent about the entry stays.
			next.Disabled, next.Category = ref.Disabled, ref.Category
		}
		next.FromCollection = true
		marks[key] = true
		entries = append(entries, next)
	}

	// Everything else, anchored after the collection entry before it.
	after := map[string][]domain.ModReference{}
	anchor := ""
	seen := map[string]bool{}
	for _, ref := range base.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		first := !seen[key]
		seen[key] = true
		if inDoc[key] {
			if first {
				anchor = key
			}
			continue
		}
		if !first {
			continue
		}
		if ref.FromCollection && c.remove[key] {
			continue
		}
		// A kept obsolete member is the user's now.
		ref.FromCollection = false
		after[anchor] = append(after[anchor], ref)
	}
	mods := slices.Clone(after[""])
	for _, ref := range entries {
		mods = append(mods, ref)
		mods = append(mods, after[domain.ModKey(ref.SourceID, ref.ModID)]...)
	}
	target.Mods = mods

	if !plan.doc.Plugins.IsZero() {
		target.Plugins = plan.doc.Plugins.Clone()
	} else {
		target.Plugins = base.Plugins.Clone()
	}
	target.Overrides = maps.Clone(base.Overrides)
	for path, content := range plan.doc.Overrides {
		if !c.allowAny && slices.ContainsFunc(plan.RestrictedOverrides, func(o RestrictedOverride) bool { return o.Path == path }) {
			// Held back: a new one is not added, and a changed one keeps
			// the profile's own content.
			continue
		}
		if target.Overrides == nil {
			target.Overrides = map[string][]byte{}
		}
		target.Overrides[path] = []byte(content)
	}
	target.External = slices.Clone(plan.doc.External)
	target.Collection = plan.info
	return &target, marks
}

// ApplyCollectionUpdate executes plan under opts: it converges the profile
// to the new version through the import's two-phase engine, then removes
// the obsolete members opts names. An UpToDate plan does nothing. It
// refuses, writing nothing, when the new version needs a feature this lmm
// lacks (*CollectionUnsupportedError), when an option names an entry the
// plan does not offer (ErrBadUpdateChoice), or when the profile or its
// installed mods changed since the plan (ErrStalePlan). An installer
// question refuses as an import's does, with the import result's
// FomodChoicesRequired, nothing written. sink may be nil.
func (s *Service) ApplyCollectionUpdate(ctx context.Context, game *domain.Game, plan *CollectionUpdatePlan, opts CollectionUpdateOptions, sink EventSink) (*CollectionUpdateResult, error) {
	result := &CollectionUpdateResult{Profile: plan.Profile, UpToDate: plan.UpToDate, Version: plan.Version, NewVersion: plan.NewVersion}
	if plan.UpToDate {
		return result, nil
	}
	if len(plan.Unsupported) > 0 {
		return result, &CollectionUnsupportedError{Unsupported: plan.Unsupported, Supported: supportedExcept(plan.Unsupported)}
	}
	choices, err := resolveUpdateChoices(plan, opts)
	if err != nil {
		return result, err
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return result, err
	}
	defer release()

	pm := s.NewProfileManager()
	current, err := pm.Get(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, err
	}
	if !sameProfileState(current, plan.base) {
		return result, fmt.Errorf("%w: profile %s changed since this update was planned", ErrStalePlan, plan.Profile)
	}
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}

	target, marks := updateTarget(plan, choices)
	data, err := config.ExportProfile(target)
	if err != nil {
		return result, fmt.Errorf("building the updated profile: %w", err)
	}
	ip, err := s.PlanImport(ctx, game, data)
	if err != nil {
		return result, err
	}
	ip.collectionState = &collectionState{collection: target.Collection, external: target.External, plugins: target.Plugins, fromCollection: marks}
	// Only what the new version adds or rewrites goes through the
	// override gate, and updateTarget applied it: the profile's own
	// overrides stay as they are.
	ip.RestrictedOverrides = nil
	added := map[string]bool{}
	for _, it := range plan.Added {
		added[it.Key()] = true
	}
	for key := range plan.autoUpdate {
		if added[key] {
			if ip.autoUpdate == nil {
				ip.autoUpdate = map[string]bool{}
			}
			ip.autoUpdate[key] = true
		}
	}
	importOpts := ProfileImportOptions{
		Force: true, Install: true, Fomod: opts.Fomod, AllowAnyOverrides: opts.AllowAnyOverrides,
		// The profile's own hooks are the target's; a document's never are.
		WithHooks:    true,
		AllOrNothing: plan.Origin == CollectionFromSource,
	}
	pending := 0
	if plan.manualDownload {
		// As a source collection's import on a free account: only what
		// needs no download is installed, and the rest is recorded pending.
		pending = len(ip.NeedsRedownload) + len(ip.Missing)
		ip.NeedsRedownload, ip.Missing = nil, nil
	}
	importResult, err := s.applyImportHeld(ctx, game, ip, importOpts, sink)
	result.Import = importResult
	if importResult != nil && importResult.ProfileName != "" && !importResult.Refused {
		if !opts.AllowAnyOverrides {
			importResult.OverridesSkipped = plan.RestrictedOverrides
		}
		if pending > 0 {
			importResult.Skipped += pending
			importResult.Notes = append(importResult.Notes, fmt.Sprintf("This account is not served downloads through the API, so %d item(s) the new version adds or moves are recorded pending: get each file from its page and install it from the file.", pending))
		}
	}
	if plan.Origin == CollectionFromSource {
		if lapsed := s.lapsedAccount(plan.SourceID, err); lapsed != nil {
			return result, lapsed
		}
	}
	if importResult == nil || importResult.ProfileName == "" || importResult.Refused {
		// Nothing was written.
		return result, err
	}
	for _, it := range plan.Added {
		if marks[it.Key()] {
			result.Added++
		}
	}
	for _, it := range plan.Changed {
		if marks[it.Key()] {
			result.Changed++
		}
	}
	for _, it := range plan.Conflicts {
		if choices.override[it.Key()] {
			result.Overridden++
		} else {
			result.Conflicts++
		}
	}
	for _, it := range plan.Removed {
		key := it.Key()
		if !choices.remove[key] {
			result.Kept++
			continue
		}
		if w := s.removeObsoleteMember(ctx, game, plan.Profile, it); w != "" {
			result.Warnings = append(result.Warnings, w)
			continue
		}
		result.Removed = append(result.Removed, key)
	}
	if plan.Origin == CollectionFromSource && plan.Version > 0 && plan.NewVersion != plan.Version {
		if w := s.dropCollectionRevision(ctx, game, plan.SourceID, plan.Slug, plan.Version); w != "" {
			result.Warnings = append(result.Warnings, w)
		}
	}
	return result, err
}

// sameProfileState reports whether a and b record the same profile state
// an update reads and writes.
func sameProfileState(a, b *domain.Profile) bool {
	return reflect.DeepEqual(a.Mods, b.Mods) && a.Plugins.Equal(b.Plugins) && a.Collection.Equal(b.Collection) &&
		reflect.DeepEqual(a.External, b.External) && reflect.DeepEqual(a.Overrides, b.Overrides)
}

// removeObsoleteMember uninstalls it from profileName, the profile already
// saved without it; "" when done, else what went wrong. A member with no
// installed row (never installed, or removed by hand) needs nothing.
func (s *Service) removeObsoleteMember(ctx context.Context, game *domain.Game, profileName string, it CollectionUpdateItem) string {
	if _, err := s.GetInstalledMod(ctx, it.SourceID, it.ModID, game.ID, profileName); err != nil {
		return ""
	}
	live, recordedOnly, err := s.profileScope(ctx, game.ID, profileName)
	if err == nil {
		if recordedOnly {
			all := func(domain.DeployPath) bool { return true }
			_, err = s.uninstallRecorded(ctx, game, profileName, it.SourceID, it.ModID, live, UninstallOptions{}, all, all)
		} else {
			_, err = s.uninstallMod(ctx, game, profileName, it.SourceID, it.ModID, UninstallOptions{})
		}
	}
	if err != nil {
		return fmt.Sprintf("could not remove %s: %v (uninstall it with `lmm uninstall %s`)", it.Key(), err, it.ModID)
	}
	return ""
}

// dropCollectionRevision removes a source collection revision's recipe
// cache entry once no profile of game names that revision; "" when done or
// still in use, else what went wrong.
func (s *Service) dropCollectionRevision(ctx context.Context, game *domain.Game, sourceID, slug string, revision int) string {
	profiles, err := s.NewProfileManager().List(ctx, game.ID)
	if err != nil {
		return ""
	}
	for _, p := range profiles {
		if c := p.Collection; c != nil && c.Origin != nil &&
			c.Origin.SourceID == sourceID && c.Origin.Slug == slug && c.Origin.Revision == revision {
			return ""
		}
	}
	dir, err := s.collectionCacheDir(game, sourceID, slug, revision)
	if err != nil {
		return ""
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Sprintf("could not remove revision %d's recipe from the cache: %v", revision, err)
	}
	return ""
}
