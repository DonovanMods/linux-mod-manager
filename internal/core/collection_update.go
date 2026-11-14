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
	// does. Both apply to a source collection only. Unless OptionalChosen,
	// an optional item the profile already has stays in without either;
	// with it, Optional is the whole selection (the web plan view's
	// toggles, #613 review), so an item the user unticks is not re-added.
	WithOptional   bool
	Optional       []string
	OptionalChosen bool
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
// list: the plugins it adds and drops, whether it reorders the plugins or
// changes the rules, the new version's summary, and the conflicts - where
// the user and the collection both changed the same thing since the
// collection last applied its list. The user's side of a conflict is kept
// unless the apply overrides it: a plugin's on/off state by
// "plugin:<name>", the order by "plugins:order", the rules by
// "plugins:rules".
type CollectionPluginChange struct {
	Added     []string                 `json:"added,omitempty"`
	Removed   []string                 `json:"removed,omitempty"`
	Reordered bool                     `json:"reordered,omitzero"`
	Rules     bool                     `json:"rules_changed,omitzero"`
	Summary   *CollectionPluginSummary `json:"summary,omitempty"`
	// Conflicts names the plugins whose on/off state both changed.
	Conflicts []string `json:"conflicts,omitempty"`
	// Moved names the plugins both reordered: the order conflicts.
	Moved []string `json:"moved,omitempty"`
	// RulesConflict is whether both changed the rules.
	RulesConflict bool `json:"rules_conflict,omitzero"`
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
	// LoadOrders is what it does to each of the profile's file load orders
	// (#620), in id order; absent when nothing.
	LoadOrders []CollectionLoadOrderChange `json:"load_orders,omitempty"`
	// OverridesAdded and OverridesChanged are the config overrides the new
	// version adds or rewrites, OverridesRemoved the collection's own it no
	// longer carries (an override the user changed is kept), and
	// OverrideConflicts the paths the user and the collection both changed,
	// whose user content is kept unless the apply overrides it
	// ("override:<path>"). RestrictedOverrides are those of them that are
	// not text config files (or are loader configuration), which the apply
	// skips unless AllowAnyOverrides.
	OverridesAdded      []string             `json:"overrides_added,omitempty"`
	OverridesChanged    []string             `json:"overrides_changed,omitempty"`
	OverridesRemoved    []string             `json:"overrides_removed,omitempty"`
	OverrideConflicts   []string             `json:"override_conflicts,omitempty"`
	RestrictedOverrides []RestrictedOverride `json:"restricted_overrides,omitempty"`
	// RestrictedPatches counts the patches the updated profile would
	// install with that need --allow-any-overrides (#610: to code, or kept
	// for another file), across the whole profile, as Apply gates them:
	// with any, Apply refuses unless the update allows any override.
	// TreeInputs lists every patch and file list it counts them in; Apply
	// admits only the patches it lists.
	RestrictedPatches int                `json:"restricted_patches,omitzero"`
	TreeInputs        []ImportTreeInputs `json:"tree_inputs,omitempty"`
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
	// bundle is the new version's container's bundled files (#623): kept
	// at Apply for the lmm-collection source, as an import keeps them.
	bundle *pendingBundle
	// patchSources names the patches the new version brings (#610), kept
	// in the store at plan and admitted at Apply; patchListed every patch
	// use the plan lists (TreeInputs), which alone --allow-any-overrides
	// admits.
	patchSources map[string]bool
	patchListed  map[string]bool
	snapshot     installedSnapshot
}

// CollectionUpdateOptions is the user's say over an update, taken at Apply
// so a frontend can show the plan and then ask.
type CollectionUpdateOptions struct {
	// Remove names obsolete members (CollectionUpdatePlan.Removed, by
	// domain.ModKey) to remove and uninstall; RemoveObsolete removes them
	// all. The rest are kept, as the user's.
	Remove         []string
	RemoveObsolete bool
	// Override names conflicts to resolve the collection's way: an entry
	// (CollectionUpdatePlan.Conflicts, by domain.ModKey) takes the new pin
	// and follows the collection again; "plugin:<name>", "plugins:order"
	// and "plugins:rules" take the collection's side of a plugin-list
	// conflict (CollectionPluginChange), "override:<path>" its content of a
	// conflicting override (OverrideConflicts), "load_order:<id>" its order
	// of a file load order (CollectionLoadOrderChange). OverrideConflicts overrides
	// them all. The rest keep the user's version.
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
	var container bool
	var bundle *collectionBundle
	var raw []byte
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
		doc, container, bundle, more, err = s.readCollectionData(data, true)
		if err != nil {
			return nil, err
		}
		raw = data
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
		doc, container, bundle, more, err = s.readCollectionData(data, false)
		if err != nil {
			return nil, err
		}
		raw = data
		warnings = append(warnings, more...)
	}
	more, err := sanitizeCollection(doc)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, more...)
	// #623: the container's bundled items, checked and given the ids and
	// versions the profile records, as an import does. #610: a converted
	// source collection's come from its recipe, kept under the recipe's id.
	var recipe *recipeFiles
	if conv != nil {
		recipe = conv.recipe
	}
	carriesFiles, bundleID := container, bundleCollectionIDFor(doc.Info.ID)
	if recipe != nil {
		bundle, carriesFiles, bundleID = recipe.bundle(), true, recipe.collectionID()
	}
	_, pending, more, err := resolveBundledItems(doc, bundle, carriesFiles, bundleID)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, more...)
	if pending != nil {
		pending.data = raw
		if recipe != nil {
			pending.data, pending.recipeDir = nil, recipe.dir
		}
		plan.bundle = pending
	}
	// #610: every patch the new version names, and the ones to code.
	broughtShas, err := s.collectionPatchSources(game, doc, bundle, recipe)
	if err != nil {
		return nil, err
	}
	// Counted over the whole profile the update saves - the new version's
	// entries and the profile's others - as its apply gates every one of
	// them (#610 re-review), so the count is what apply refuses.
	newRefs := make([]domain.ModReference, len(doc.Items))
	for i, item := range doc.Items {
		newRefs[i] = item.Ref
	}
	whole := slices.Clone(newRefs)
	newUses := listedPatches(newRefs)
	for _, ref := range base.Mods {
		// The profile's own entries whose patches the new version does not
		// name as they are - the user's, kept or in conflict.
		for use := range listedPatches([]domain.ModReference{ref}) {
			if !newUses[use] {
				whole = append(whole, ref)
				break
			}
		}
	}
	plan.patchSources = broughtUses(newRefs, broughtShas)
	plan.TreeInputs = s.importTreeInputs(game, whole, plan.patchSources)
	plan.RestrictedPatches, _ = restrictedTreePatches(plan.TreeInputs)
	plan.patchListed = listedPatches(whole)

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
	switch {
	case fetchedVersion == 0 && plan.Version > 0:
		// A document that names no version cannot be newer than one the
		// profile has (#613 review).
		return nil, &CollectionUpdateMismatchError{Code: CollectionUpdateOlderVersion, ID: plan.ID, Version: plan.Version, FetchedID: fetchedID, FetchedVersion: fetchedVersion}
	case fetchedVersion == 0:
		warnings = append(warnings, "neither the profile nor the document names a collection version, so lmm cannot tell whether this is a newer one: the plan compares them entry by entry")
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

	// The game the document names, against the profile's (as an import
	// warns when the game it was asked for contradicts the document).
	if w := s.gameMismatch(doc.Game, game); w != "" && doc.FormatVersion > 0 {
		warnings = append(warnings, w)
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
	plan.Unsupported = unsupportedFeatures(doc.Requires, deriveCollectionRequires(doc), s.modTypeRoutes(game), s.loadOrderSupport(game))
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
	if b := plan.bundle; b != nil {
		// #623: a bundled item holding code is held back unless the update
		// allows any override, as an import holds it back.
		for _, item := range slices.Sorted(maps.Values(b.restricted)) {
			plan.RestrictedOverrides = append(plan.RestrictedOverrides, RestrictedOverride{
				Path:   "files/" + item + "/",
				Reason: "a bundled item holding native code, a system script or a loader configuration",
			})
		}
	}
	if w := pluginRuleCycle(doc.Plugins); w != "" {
		warnings = append(warnings, w)
	}
	plan.Warnings = warnings
	plan.NoChanges = len(plan.Added) == 0 && len(plan.Changed) == 0 && len(plan.Removed) == 0 && len(plan.Conflicts) == 0 &&
		plan.Plugins == nil && len(plan.OverridesAdded) == 0 && len(plan.OverridesChanged) == 0 &&
		len(plan.OverridesRemoved) == 0 && len(plan.OverrideConflicts) == 0 && len(plan.LoadOrders) == 0 &&
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
	if err != nil || opts.WithOptional || req.OptionalChosen {
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

// diffRest fills the plan's plugin, override and external-item changes:
// the three-way merges (collection_update_merge.go) with no conflict
// overridden, so every conflict is named.
func (p *CollectionUpdatePlan) diffRest(s *Service, game *domain.Game) {
	none := func(string) bool { return false }
	b, hasBase := baselineOf(p.base)
	if pm := mergePlugins(p.base.Plugins, b.Plugins, p.doc.Plugins, hasBase, none); !pm.result.Equal(p.base.Plugins) || len(pm.enabled)+len(pm.moved) > 0 || pm.rules {
		change := &CollectionPluginChange{Added: pm.added, Removed: pm.dropped, Conflicts: pm.enabled, Moved: pm.moved, RulesConflict: pm.rules}
		var before, after []string
		kept := map[string]bool{}
		for _, e := range pm.result.Order {
			kept[pluginKey(e.Name)] = true
		}
		had := map[string]bool{}
		for _, e := range p.base.Plugins.Order {
			had[pluginKey(e.Name)] = true
			if kept[pluginKey(e.Name)] {
				before = append(before, pluginKey(e.Name))
			}
		}
		for _, e := range pm.result.Order {
			if had[pluginKey(e.Name)] {
				after = append(after, pluginKey(e.Name))
			}
		}
		change.Reordered = !slices.Equal(before, after)
		change.Rules = !reflect.DeepEqual(p.base.Plugins.Rules, pm.result.Rules)
		_, managed := s.pluginRules(game)
		change.Summary = summarisePlugins(p.doc.Plugins, managed)
		p.Plugins = change
	}

	_, p.LoadOrders = mergeLoadOrders(p.base.LoadOrders, b.LoadOrders, p.doc.LoadOrders, hasBase, none)

	om := mergeOverrides(p.base.Overrides, b.Overrides, hasBase, p.doc.Overrides, none, true)
	p.OverridesAdded, p.OverridesChanged, p.OverridesRemoved, p.OverrideConflicts = om.added, om.changed, om.removed, om.conflicts
	for _, list := range [][]string{om.added, om.changed, om.conflicts} {
		for _, path := range list {
			if reason := restrictedOverrideReason(path); reason != "" {
				p.RestrictedOverrides = append(p.RestrictedOverrides, RestrictedOverride{Path: path, Reason: reason})
			}
		}
	}
	sort.Slice(p.RestrictedOverrides, func(i, j int) bool { return p.RestrictedOverrides[i].Path < p.RestrictedOverrides[j].Path })

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
	itemKeys := func(items []CollectionUpdateItem) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.Key()
		}
		return out
	}
	pick := func(what string, all bool, named []string, valid []string, into map[string]bool) error {
		keys := map[string]bool{}
		for _, key := range valid {
			keys[key] = true
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
	if err := pick("obsolete members", opts.RemoveObsolete, opts.Remove, itemKeys(plan.Removed), c.remove); err != nil {
		return c, err
	}
	if err := pick("conflicts", opts.OverrideConflicts, opts.Override, plan.ConflictKeys(), c.override); err != nil {
		return c, err
	}
	return c, nil
}

// ConflictKeys is every conflict key the plan offers to override
// (CollectionUpdateOptions.Override): each conflicting entry's
// domain.ModKey, then its plugin list's and its overrides'.
func (p *CollectionUpdatePlan) ConflictKeys() []string {
	keys := make([]string, 0, len(p.Conflicts))
	for _, it := range p.Conflicts {
		keys = append(keys, it.Key())
	}
	return append(keys, p.conflictKeys()...)
}

// conflictKeys are the plan's conflict keys beyond its entries': its
// plugin list's and its overrides'.
func (p *CollectionUpdatePlan) conflictKeys() []string {
	var keys []string
	if pc := p.Plugins; pc != nil {
		for _, name := range pc.Conflicts {
			keys = append(keys, collectionPluginKey(name))
		}
		if len(pc.Moved) > 0 {
			keys = append(keys, collectionPluginOrderKey)
		}
		if pc.RulesConflict {
			keys = append(keys, collectionPluginRulesKey)
		}
	}
	for _, lo := range p.LoadOrders {
		if len(lo.Moved) > 0 {
			keys = append(keys, collectionLoadOrderKey(lo.ID))
		}
	}
	for _, path := range p.OverrideConflicts {
		keys = append(keys, collectionOverrideKey(path))
	}
	return keys
}

// overrides reports whether the caller resolves the conflict key the
// collection's way.
func (c updateChoices) overrides(key string) bool { return c.override[key] }

// ErrBadUpdateChoice refuses an update option naming an entry the plan
// does not offer the choice for: the caller's input.
var ErrBadUpdateChoice = errors.New("invalid collection update choice")

// updateTarget is the profile plan leads to under c, built on base - the
// profile as it stands at Apply, which sameProfileState held equal to the
// plan's in everything the update reads, so an edit the update does not
// touch (a hook, the link method) is kept - and the entries it marks as the
// collection's. The collection's entries come in the new version's order;
// every other entry the profile has stays just after the collection entry
// it followed, so a mod the user placed keeps its place among them. That
// includes the user's own entries, kept obsolete members, an obsolete member
// chosen for removal (unmarked, until its uninstall removes it with its
// installed row, so the profile never loses a mod still installed) and every
// repeated reference (a hand edit: a mod listed twice is decided by its
// first reference, and the copies are kept as written).
func updateTarget(plan *CollectionUpdatePlan, base *domain.Profile, c updateChoices) (*domain.Profile, map[string]bool, []RestrictedOverride) {
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
		if inDoc[key] && first {
			anchor = key
			continue
		}
		if first {
			// A kept obsolete member is the user's now, and so is one
			// chosen for removal until its uninstall takes it out.
			ref.FromCollection = false
		}
		after[anchor] = append(after[anchor], ref)
	}
	mods := slices.Clone(after[""])
	for _, ref := range entries {
		mods = append(mods, ref)
		mods = append(mods, after[domain.ModKey(ref.SourceID, ref.ModID)]...)
	}
	target.Mods = mods

	target.Plugins, target.CollectionBaseline = mergeCollectionPlugins(plan, base, c)
	b, hasBase := baselineOf(base)
	target.LoadOrders, _ = mergeLoadOrders(base.LoadOrders, b.LoadOrders, plan.doc.LoadOrders, hasBase, c.overrides)
	var heldBack []RestrictedOverride
	target.Overrides, heldBack = mergeCollectionOverrides(plan, base, c, target.CollectionBaseline)
	target.External = slices.Clone(plan.doc.External)
	target.Collection = plan.info
	return &target, marks, heldBack
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
	// #610: a patch to code is refused, not dropped, as at an import.
	if plan.RestrictedPatches > 0 && !opts.AllowAnyOverrides {
		return result, fmt.Errorf("%w: %d of the new version's patches change code or apply to a file no import admitted them for; update with --allow-any-overrides to apply them", ErrCodePatch, plan.RestrictedPatches)
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

	// #623: a bundled item holding code that the update does not allow
	// stays as the profile has it: not added when new, not moved when the
	// profile already has it, so nothing installed is left unlisted.
	var bundledSkipped []string
	targetPlan := plan
	if b := plan.bundle; b != nil && !opts.AllowAnyOverrides && len(b.restricted) > 0 {
		have := firstRefs(current.Mods)
		doc := *plan.doc
		doc.Items = nil
		for _, item := range plan.doc.Items {
			key := domain.ModKey(item.Ref.SourceID, item.Ref.ModID)
			if name, held := b.restricted[key]; held {
				ref, ok := have[key]
				// Skipped is what the hold kept from moving: an item the
				// profile already has at this version moved nowhere.
				if !ok || ref.Version != item.Ref.Version {
					bundledSkipped = append(bundledSkipped, name)
				}
				if !ok {
					continue
				}
				item.Ref = ref
			}
			doc.Items = append(doc.Items, item)
		}
		sort.Strings(bundledSkipped)
		copied := *plan
		copied.doc = &doc
		targetPlan = &copied
	}
	target, marks, heldBack := updateTarget(targetPlan, current, choices)
	data, err := config.ExportProfile(target)
	if err != nil {
		return result, fmt.Errorf("building the updated profile: %w", err)
	}
	// The target names the collection's bundled items (#623): the update
	// keeps their container below, as an import does. Only this
	// container's own items, and the ones the profile already has, may
	// plan; any other bundled reference is refused.
	ip, err := s.planImport(ctx, game, data, bundledRefAllowed(plan.bundle, current))
	if err != nil {
		return result, err
	}
	ip.collectionState = &collectionState{collection: target.Collection, external: target.External, plugins: target.Plugins,
		loadOrders: target.LoadOrders, fromCollection: marks, baseline: target.CollectionBaseline}
	// #609: the new version's recorded installer choices replay only
	// against the curator's archive, as at an import.
	ip.archiveMD5 = collectionArchiveMD5s(plan.doc)
	// #610: the patches the new version brings are admitted by this update.
	ip.patchSources = plan.patchSources
	// --allow-any-overrides admits only what the plan listed
	// (TreeInputs), never a patch it did not show.
	ip.patchListed = plan.patchListed
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
		// #610: a bundled item needs no download.
		keep := slices.Concat(bundledOnly(ip.NeedsRedownload), bundledOnly(ip.Missing))
		pending = len(ip.NeedsRedownload) + len(ip.Missing) - len(keep)
		ip.NeedsRedownload, ip.Missing = bundledOnly(ip.NeedsRedownload), bundledOnly(ip.Missing)
	}
	if b := plan.bundle; b != nil {
		if err := s.keepPendingBundle(game, b, opts.AllowAnyOverrides); err != nil {
			return result, err
		}
	}
	importResult, err := s.applyImportHeld(ctx, game, ip, importOpts, sink)
	result.Import = importResult
	if importResult != nil && importResult.ProfileName != "" && !importResult.Refused {
		importResult.OverridesSkipped = heldBack
		importResult.BundledSkipped = bundledSkipped
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
	for _, key := range plan.ConflictKeys() {
		if choices.override[key] {
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
		if cerr := ctx.Err(); cerr != nil {
			// Every member not yet removed stays in the profile, installed,
			// as the user's: nothing is left half-removed.
			return result, cerr
		}
		if rerr := s.removeObsoleteMember(ctx, game, plan.Profile, it); rerr != nil {
			if cerr := ctx.Err(); cerr != nil {
				return result, cerr
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("could not remove %s: %v; it stays in the profile, installed, as your own mod (uninstall it with `lmm uninstall %s`)", describeUpdateItem(it), rerr, it.ModID))
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

// bundledRefAllowed is which bundled references an update's target may
// name (#623): one of b's own items (the new container, kept by the
// update), or one current - the profile as it stands on disk - already
// records at that version, whose container an earlier import kept.
func bundledRefAllowed(b *pendingBundle, current *domain.Profile) func(domain.ModReference) bool {
	recorded := map[string]bool{}
	for _, ref := range current.Mods {
		if ref.SourceID == domain.SourceCollection {
			recorded[domain.ModKey(ref.SourceID, ref.ModID)+"@"+ref.Version] = true
		}
	}
	return func(ref domain.ModReference) bool {
		return b.carries(ref) || recorded[domain.ModKey(ref.SourceID, ref.ModID)+"@"+ref.Version]
	}
}

// sameProfileState reports whether a and b record the same profile state
// an update reads and writes.
func sameProfileState(a, b *domain.Profile) bool {
	return reflect.DeepEqual(a.Mods, b.Mods) && a.Plugins.Equal(b.Plugins) && a.Collection.Equal(b.Collection) &&
		reflect.DeepEqual(a.External, b.External) && reflect.DeepEqual(a.Overrides, b.Overrides) &&
		reflect.DeepEqual(a.CollectionBaseline, b.CollectionBaseline)
}

// removeObsoleteMember uninstalls it from profileName: its installed row,
// its deployed files and its profile reference, in the uninstall's own
// order (Ruling 16: the reference goes after the row, even under a
// cancelled ctx). A member with no installed row (never installed, or
// uninstalled by hand) has only its reference to remove. On an error the
// member stays as it was: in the profile, and installed when it was.
func (s *Service) removeObsoleteMember(ctx context.Context, game *domain.Game, profileName string, it CollectionUpdateItem) error {
	if _, err := s.GetInstalledMod(ctx, it.SourceID, it.ModID, game.ID, profileName); err != nil {
		if !errors.Is(err, domain.ErrModNotFound) {
			return err
		}
		return s.NewProfileManager().RemoveMod(ctx, game.ID, profileName, it.SourceID, it.ModID)
	}
	live, recordedOnly, err := s.profileScope(ctx, game.ID, profileName)
	if err != nil {
		return err
	}
	if recordedOnly {
		all := func(domain.DeployPath) bool { return true }
		_, err = s.uninstallRecorded(ctx, game, profileName, it.SourceID, it.ModID, live, UninstallOptions{}, all, all)
		return err
	}
	_, err = s.uninstallMod(ctx, game, profileName, it.SourceID, it.ModID, UninstallOptions{})
	return err
}

// describeUpdateItem names an update entry for a message.
func describeUpdateItem(it CollectionUpdateItem) string {
	if it.Name != "" {
		return it.Name + " (" + it.Key() + ")"
	}
	return it.Key()
}

// dropCollectionRevision removes a source collection revision's recipe
// cache entry once no profile of game names that revision; "" when done or
// still in use, else what went wrong.
func (s *Service) dropCollectionRevision(ctx context.Context, game *domain.Game, sourceID, slug string, revision int) string {
	dir, err := s.collectionCacheDir(game, sourceID, slug, revision)
	if err != nil {
		return ""
	}
	// Every game whose recipes live in the same place - a cache_path two
	// games share (#613 review) - is asked, not only this one.
	for _, g := range s.ListGames() {
		if other, err := s.collectionCacheDir(g, sourceID, slug, revision); err != nil || other != dir {
			continue
		}
		profiles, err := s.NewProfileManager().List(ctx, g.ID)
		if err != nil {
			return ""
		}
		for _, p := range profiles {
			if c := p.Collection; c != nil && c.Origin != nil &&
				c.Origin.SourceID == sourceID && c.Origin.Slug == slug && c.Origin.Revision == revision {
				return ""
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Sprintf("could not remove revision %d's recipe from the cache: %v", revision, err)
	}
	return ""
}
