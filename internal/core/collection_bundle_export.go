// collection_bundle_export.go is the export half of bundled collection
// files (#623): `lmm profile export --bundle <mod>` and the web UI's
// export checkboxes. An author names the profile's mods whose files are
// their own - a mod imported from a file (`lmm import`, source local) - and
// the export carries each one's installed files in the `.lmmc` container
// under files/<item>/, its mods: entry becoming a bundled item. A mod a
// collection already bundled (source lmm-collection) is always carried
// again, since a recipient has nowhere else to get it.
//
// lmm never bundles a source's mod: one installed from NexusMods,
// CurseForge or any other source is refused by name, since the recipient
// downloads it from there with their own account.
//
// The container is checked with the importer's own reader before it is
// handed back, so an export never makes a container an import refuses.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// ErrBundleSourceMod refuses bundling a mod that came from a mod source:
// lmm never redistributes a source's files.
var ErrBundleSourceMod = errors.New("lmm never bundles a mod that came from a mod source")

// ErrBundleModNotFound is a --bundle name that matches no mod of the
// profile, or more than one.
var ErrBundleModNotFound = errors.New("no single mod of the profile matches")

// IsBundleRefused reports whether err refused an export's --bundle
// choice: a mod from a source, a name that matches no mod, or files that
// cannot be bundled. It is the caller's input.
func IsBundleRefused(err error) bool {
	return errors.Is(err, ErrBundleSourceMod) || errors.Is(err, ErrBundleModNotFound) ||
		errors.Is(err, ErrInvalidBundle) || errors.Is(err, ErrBundleTooLarge)
}

// BundleCandidate is a mod of a profile whose files an export can bundle:
// a mod imported from a file, or one a collection already bundled.
type BundleCandidate struct {
	// SourceID and ModID identify the mod; Name is its display name.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
	// Always is true for a mod a collection already bundled: every
	// container export carries it again.
	Always bool `json:"always,omitzero"`
}

// BundleCandidates lists gameID/profileName's mods an export can bundle, in
// the profile's order: what `--bundle` accepts, and what the web UI's
// export offers.
func (s *Service) BundleCandidates(ctx context.Context, gameID, profileName string) ([]BundleCandidate, error) {
	profile, err := s.NewProfileManager().loadForExport(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	rows := s.installedRowsByKey(ctx, gameID, profileName)
	out := []BundleCandidate{}
	for _, ref := range profile.Mods {
		if ref.SourceID != domain.SourceLocal && ref.SourceID != domain.SourceCollection {
			continue
		}
		name := rows[domain.ModKey(ref.SourceID, ref.ModID)].Name
		out = append(out, BundleCandidate{SourceID: ref.SourceID, ModID: ref.ModID, Name: name, Always: ref.SourceID == domain.SourceCollection})
	}
	return out, nil
}

// installedRowsByKey is profileName's installed rows by mod key; empty
// when they cannot be read.
func (s *Service) installedRowsByKey(ctx context.Context, gameID, profileName string) map[string]domain.InstalledMod {
	out := map[string]domain.InstalledMod{}
	if rows, err := s.GetInstalledMods(ctx, gameID, profileName); err == nil {
		for _, row := range rows {
			out[domain.ModKey(row.SourceID, row.ID)] = row
		}
	}
	return out
}

// exportBundle is one item an export bundles: the item's directory name
// and its files, read from lmm's cache.
type exportBundle struct {
	item *bundledItem
}

// resolveBundleChoice finds the profile mod a --bundle name means:
// "source:id", else a mod id, else a display name (in any case) - each
// only when exactly one mod matches.
func resolveBundleChoice(choice string, refs []domain.ModReference, rows map[string]domain.InstalledMod) (domain.ModReference, error) {
	choice = strings.TrimSpace(choice)
	var hits []domain.ModReference
	for _, match := range []func(domain.ModReference) bool{
		func(r domain.ModReference) bool { return domain.ModKey(r.SourceID, r.ModID) == choice },
		func(r domain.ModReference) bool { return r.ModID == choice },
		func(r domain.ModReference) bool {
			name := rows[domain.ModKey(r.SourceID, r.ModID)].Name
			return name != "" && strings.EqualFold(name, choice)
		},
	} {
		hits = slices.DeleteFunc(slices.Clone(refs), func(r domain.ModReference) bool { return !match(r) })
		if len(hits) > 0 {
			break
		}
	}
	if len(hits) != 1 {
		return domain.ModReference{}, fmt.Errorf("%w %q (name a mod as source:id, its id, or its name)", ErrBundleModNotFound, choice)
	}
	return hits[0], nil
}

// bundleItems resolves an export's bundled items: every lmm-collection mod
// of the profile, and each mod choices names, which must be a mod imported
// from a file. It rewrites each one's document entry to a bundled item and
// returns the items with their files. collectionID is the document's id.
func (s *Service) bundleItems(ctx context.Context, game *domain.Game, profileName, collectionID string, doc *domain.CollectionDocument, choices []string) ([]exportBundle, error) {
	rows := s.installedRowsByKey(ctx, game.ID, profileName)
	refs := make([]domain.ModReference, len(doc.Items))
	for i, item := range doc.Items {
		refs[i] = item.Ref
	}
	chosen := map[string]bool{}
	for _, choice := range choices {
		ref, err := resolveBundleChoice(choice, refs, rows)
		if err != nil {
			return nil, err
		}
		if ref.SourceID != domain.SourceLocal && ref.SourceID != domain.SourceCollection {
			name := rows[domain.ModKey(ref.SourceID, ref.ModID)].Name
			if name == "" {
				name = ref.ModID
			}
			return nil, fmt.Errorf("%w: %s came from %s, so a recipient downloads it from there; bundle only files of your own, imported with lmm import", ErrBundleSourceMod, name, s.sourceDisplayName(ref.SourceID))
		}
		chosen[domain.ModKey(ref.SourceID, ref.ModID)] = true
	}

	var out []exportBundle
	used := map[string]bool{}
	for i := range doc.Items {
		ref := &doc.Items[i].Ref
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if ref.SourceID != domain.SourceCollection && !chosen[key] {
			continue
		}
		if !validBundleCollectionID(collectionID) {
			return nil, fmt.Errorf("%w: a collection with bundled items needs an id of letters, digits, '-' and '_' (this one's is %q)", ErrInvalidBundle, collectionID)
		}
		row, hasRow := rows[key]
		item, err := s.bundleSource(game, *ref, row, hasRow)
		if err != nil {
			return nil, fmt.Errorf("bundling %s: %w", firstNonEmpty(row.Name, doc.Items[i].Name, key), err)
		}
		item.name = bundleItemName(*ref, row.Name, used)
		used[item.name] = true
		if doc.Items[i].Name == "" {
			doc.Items[i].Name = firstNonEmpty(row.Name, item.display)
		}
		modType, category, disabled, locked := ref.ModType, ref.Category, ref.Disabled, ref.Locked
		*ref = domain.ModReference{
			SourceID: domain.SourceCollection, ModID: item.name, Version: item.digest,
			ModType: modType, Category: category, Disabled: disabled, Locked: locked,
		}
		doc.Items[i].Files = nil
		out = append(out, exportBundle{item: item})
	}
	return out, nil
}

// bundleSource reads the files an export bundles for ref: a bundled item's
// from the container its collection was imported from (what its author
// made, whether or not it is installed), else - and for a mod imported from
// a file - the installed files in lmm's cache.
func (s *Service) bundleSource(game *domain.Game, ref domain.ModReference, row domain.InstalledMod, hasRow bool) (*bundledItem, error) {
	if ref.SourceID == domain.SourceCollection {
		if item, err := s.findBundledItem(game, ref.ModID, ref.Version); err == nil {
			copied := *item
			return &copied, nil
		}
	}
	if !hasRow {
		return nil, fmt.Errorf("%w: it has no installed files to bundle (install or enable it first)", ErrInvalidBundle)
	}
	entry := cache.Key{GameID: game.ID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version, Variant: row.Variant}
	return bundledItemFromCache(s.GetGameCache(game), entry)
}

// bundleItemName is the files/<item>/ name an export gives a bundled mod:
// the item it already has when a collection bundled it, else a slug of its
// name, made unique among used.
func bundleItemName(ref domain.ModReference, name string, used map[string]bool) string {
	base := ""
	if ref.SourceID == domain.SourceCollection {
		if _, item, ok := splitBundledModID(ref.ModID); ok {
			base = item
		}
	}
	if base == "" {
		base = slugify(name)
		if len(base) > 56 {
			base = strings.TrimRight(base[:56], "-")
		}
	}
	if !validBundleItem(base) {
		base = "files"
	}
	candidate := base
	for n := 2; used[candidate]; n++ {
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	return candidate
}

// bundledItemFromCache reads a cache entry's mod files as a bundled item -
// the same reading, digest and code check an import gives a container's -
// refusing a name an import would refuse.
func bundledItemFromCache(gameCache *cache.Cache, key cache.Key) (*bundledItem, error) {
	dir := gameCache.ModPath(key)
	if !gameCache.Exists(key) {
		return nil, fmt.Errorf("%w: its installed files are not in lmm's cache (lmm verify --fix repairs it)", ErrInvalidBundle)
	}
	rels, err := gameCache.ListFiles(key)
	if err != nil {
		return nil, fmt.Errorf("%w: its installed files cannot be read (lmm verify --fix repairs a damaged cache): %v", ErrInvalidBundle, err)
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("%w: it has no installed files (lmm verify --fix repairs a damaged cache)", ErrInvalidBundle)
	}
	item := &bundledItem{}
	for _, rel := range rels {
		slash := filepath.ToSlash(rel)
		if err := checkBundleMemberName(slash); err != nil {
			return nil, err
		}
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBundle, err)
		}
		if !info.Mode().IsRegular() {
			continue // ListFiles skips links; anything else is not content
		}
		if info.Size() > maxBundleFileBytes {
			return nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit for one bundled file", ErrBundleTooLarge, slash, info.Size(), maxBundleFileBytes)
		}
		item.members = append(item.members, bundleMember{rel: slash, size: info.Size(), open: func() (io.ReadCloser, error) { return os.Open(full) }})
		item.bytes += info.Size()
	}
	if err := item.read(); err != nil {
		return nil, err
	}
	return item, nil
}

// containerFiles lists bundles' files as container members, in item and
// path order.
func containerFiles(bundles []exportBundle) []containerFile {
	var out []containerFile
	for _, b := range bundles {
		for _, m := range b.item.members {
			out = append(out, containerFile{name: collectionFilesDir + b.item.name + "/" + m.rel, open: m.open})
		}
	}
	return out
}

// checkExportedContainer reads back a container an export made, with the
// importer's reader, and checks it holds each bundle at the version its
// document records: what an export hands out, an import accepts.
func checkExportedContainer(data []byte, bundles []exportBundle) error {
	_, bundle, _, err := readCollectionContainer(data)
	if err != nil {
		return err
	}
	for _, b := range bundles {
		got := bundle.items[b.item.name]
		if got == nil || got.digest != b.item.digest {
			return fmt.Errorf("%w: files/%s/ changed while it was being exported; export again", ErrInvalidBundle, b.item.name)
		}
	}
	return nil
}

// bundleNotes is what an export with bundles tells its author.
func bundleNotes(bundles []exportBundle) []string {
	if len(bundles) == 0 {
		return nil
	}
	names := make([]string, len(bundles))
	var code []string
	for i, b := range bundles {
		names[i] = b.item.name
		if len(b.item.restricted) > 0 {
			code = append(code, b.item.name)
		}
	}
	notes := []string{fmt.Sprintf("The collection carries the files of %d mod(s) as its own (%s): share only files you made or may share.", len(bundles), strings.Join(names, ", "))}
	if len(code) > 0 {
		notes = append(notes, fmt.Sprintf("%s hold(s) code (a DLL, an executable, a script or a loader configuration): a recipient imports it only with --allow-any-overrides.", strings.Join(code, ", ")))
	}
	return notes
}
