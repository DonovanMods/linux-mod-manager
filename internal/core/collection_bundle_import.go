// collection_bundle_import.go is the import half of bundled collection files
// (#623): a mods: entry whose source is lmm-collection names a files/<item>/
// tree in the same `.lmmc` container. The plan checks each one against the
// container, gives it its profile id ("<collection id>.<item>") and its
// version (the tree's digest), and lists it with what it holds; the apply
// keeps the container - the lmm-collection source's only "download" - and
// installs the items through that source like any other mod.
package core

import (
	"fmt"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// CollectionBundledItem is one bundled item of a collection import plan:
// what the container carries for it, and whether the import holds it back.
type CollectionBundledItem struct {
	// Item names the item's directory, files/<item>/; ModID is the id the
	// profile records it under ("<collection id>.<item>"), with the source
	// lmm-collection.
	Item  string `json:"item"`
	ModID string `json:"mod_id"`
	// Name is the item's display name, when the document gives one.
	Name string `json:"name,omitempty"`
	// Version is the digest of the item's files, which the profile records
	// as its version.
	Version string `json:"version"`
	// Files and Bytes count what it holds.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// Restricted lists the files that make the import hold the item back:
	// native code, a system script or a loader configuration. An item with
	// any is imported only with --allow-any-overrides.
	Restricted []RestrictedOverride `json:"restricted,omitempty"`
}

// pendingBundle is what a collection plan's apply needs of its container's
// bundled files: the container itself, kept for the lmm-collection source,
// and the mod keys held back unless the import allows any override.
type pendingBundle struct {
	collectionID string
	data         []byte
	// recipeDir is a converted source collection's recipe entry (#610):
	// its bundled/ trees are the items' "download", kept with an index
	// beside it, and data is nil.
	recipeDir string
	bundle    *collectionBundle
	// names maps each item a mods: entry names to its display name; only
	// a named item can be admitted (collection_bundle_keep.go).
	names      map[string]string
	restricted map[string]string // mod key -> item
}

// admitted is the items an apply lets in: every named item, but one
// holding code only when anyOverrides.
func (p *pendingBundle) admitted(anyOverrides bool) map[string]bool {
	held := map[string]bool{}
	for _, item := range p.restricted {
		held[item] = true
	}
	out := map[string]bool{}
	for item := range p.names {
		if anyOverrides || !held[item] {
			out[item] = true
		}
	}
	return out
}

// resolveBundledItems checks doc's bundled items against the container's
// bundle and rewrites each to the reference the profile records: mod id
// "<collection id>.<item>", version and file id the tree's digest. A
// document that is not a container, an item the container does not hold, a
// version its files do not hash to, an item name or collection id that
// cannot name one, and an item carrying FOMOD choices are refused: the
// caller's input. Items of the container no entry names are left out, with
// a warning.
//
// collectionID is the id the items are kept under: the document's own for
// a container, the recipe's ("<source>-<slug>") for a converted source
// collection (#610).
func resolveBundledItems(doc *domain.CollectionDocument, bundle *collectionBundle, container bool, collectionID string) ([]CollectionBundledItem, *pendingBundle, []string, error) {
	var out []CollectionBundledItem
	named := map[string]bool{}
	for i := range doc.Items {
		ref := &doc.Items[i].Ref
		if ref.SourceID != domain.SourceCollection {
			continue
		}
		item := ref.ModID
		switch {
		case !container:
			return nil, nil, nil, fmt.Errorf("%w: the mod %q is bundled (files/%s/ of a .lmmc container), and a plain YAML document cannot carry files; import the .lmmc container it came from", ErrInvalidCollection, item, item)
		case !validBundleItem(item):
			return nil, nil, nil, fmt.Errorf("%w: %q cannot name a bundled item: 1 to 64 letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidBundle, item)
		case !validBundleCollectionID(collectionID):
			return nil, nil, nil, fmt.Errorf("%w: a collection with bundled items needs an id of letters, digits, '-' and '_' (this one's is %q)", ErrInvalidBundle, collectionID)
		case !ref.FomodRecord().IsZero():
			return nil, nil, nil, fmt.Errorf("%w: the bundled item %q records FOMOD installer choices; a bundled item is installed as its files are", ErrInvalidBundle, item)
		case !ref.FileList.IsZero() || !ref.Patches.IsZero():
			return nil, nil, nil, fmt.Errorf("%w: the bundled item %q records a file list or patches; a bundled item is installed as its files are", ErrInvalidBundle, item)
		}
		bi := bundle.item(item)
		if bi == nil {
			return nil, nil, nil, fmt.Errorf("%w: the mod %q is bundled, but the container holds no files/%s/", ErrInvalidBundle, item, item)
		}
		if ref.Version != "" && ref.Version != bi.digest {
			return nil, nil, nil, fmt.Errorf("%w: files/%s/ does not match the version the document records for it (%s; its files are %s): the container was changed after it was made", ErrInvalidBundle, item, ref.Version, bi.digest)
		}
		ref.ModID = BundledModID(collectionID, item)
		ref.Version = bi.digest
		ref.FileIDs = []string{bi.digest}
		if named[item] {
			continue // a repeated entry: PlanImport decides by the first
		}
		named[item] = true
		out = append(out, CollectionBundledItem{
			Item: item, ModID: ref.ModID, Name: doc.Items[i].Name, Version: bi.digest,
			Files: len(bi.members), Bytes: bi.bytes, Restricted: slices.Clone(bi.restricted),
		})
	}
	var warnings []string
	for _, name := range bundle.names() {
		if !named[name] {
			warnings = append(warnings, fmt.Sprintf("the container's files/%s/ is named by no mods: entry, so it was not imported", name))
		}
	}
	if len(out) == 0 {
		return nil, nil, warnings, nil
	}
	pending := &pendingBundle{collectionID: collectionID, bundle: bundle, names: map[string]string{}, restricted: map[string]string{}}
	for _, b := range out {
		pending.names[b.Item] = b.Name
		if len(b.Restricted) > 0 {
			pending.restricted[domain.ModKey(domain.SourceCollection, b.ModID)] = b.Item
		}
	}
	return out, pending, warnings, nil
}

// withoutMods is plan with the mods keys names taken out of every bucket
// and of the profile it saves, for an apply that holds them back.
func (p *ImportPlan) withoutMods(keys map[string]string) *ImportPlan {
	if len(keys) == 0 {
		return p
	}
	keep := func(refs []domain.ModReference) []domain.ModReference {
		return slices.DeleteFunc(slices.Clone(refs), func(r domain.ModReference) bool {
			_, drop := keys[domain.ModKey(r.SourceID, r.ModID)]
			return drop
		})
	}
	trimmed := *p
	trimmed.Installed = keep(p.Installed)
	trimmed.AlreadyCached = keep(p.AlreadyCached)
	trimmed.NeedsRedownload = keep(p.NeedsRedownload)
	trimmed.Missing = keep(p.Missing)
	state := collectionState{}
	if p.collectionState != nil {
		state = *p.collectionState
	}
	state.dropMods = keys
	trimmed.collectionState = &state
	return &trimmed
}

// refuseBundledRefs refuses a profile document naming a collection's
// bundled item (#623 review B1): one comes in only with the container that
// carries it, whose import decides whether it is admitted.
func refuseBundledRefs(refs []domain.ModReference) error {
	for _, ref := range refs {
		if ref.SourceID == domain.SourceCollection {
			return fmt.Errorf("%w: it names %s:%s, a collection's bundled item, which comes in only with the collection's .lmmc container; import the container", ErrUnsafeImport, ref.SourceID, ref.ModID)
		}
	}
	return nil
}
