// collection_bundle_keep.go is where the lmm-collection source's
// "downloads" live (#623): the `.lmmc` containers an import keeps, each
// with a sidecar index of the items it ADMITTED.
//
// A container can hold more than an import let in: an item no mods: entry
// names, or one holding code that was held back (no AllowAnyOverrides). The
// source serves an item only when an import admitted it, so a profile
// document naming a held-back item - a version-0 export, a hand edit, a
// later `lmm install` - can never bring it in by the back door
// (#623 review B1). A later import of the same container that does admit
// it (with --allow-any-overrides) adds it to the index.
//
// The index also records each item's full 256-bit digest, so a rebuild
// reads and hashes only the item asked for (not every kept container in
// full) and checks it against the whole digest, not the version's prefix.
//
// Layout, beside the Nexus recipes (collectionCacheDir):
//
//	<cache root>/collections/lmm-<collection id>/<sha>.lmmc   the container
//	<cache root>/collections/lmm-<collection id>/<sha>.json   its index
//
// where <sha> is the first 32 hex digits of the container's sha256.
// Nothing removes them: lmm has no cache-prune command yet (the Nexus
// recipes are kept the same way).
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// keptIndexSuffix is a kept container's index's extension.
const keptIndexSuffix = ".json"

// keptIndex is a kept container's sidecar: the container's sha256 and every
// item it holds, admitted or not.
type keptIndex struct {
	// Container is the container file's sha256 (hex).
	Container string `json:"container"`
	// Items are the container's bundled items, by name.
	Items map[string]keptItem `json:"items"`
}

// keptItem is one item of a kept container's index.
type keptItem struct {
	// Name is the display name the document gives the item.
	Name string `json:"name,omitempty"`
	// Version is the item's version; Digest its whole sha256 digest.
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"`
	// Admitted is whether an import let the item in: named by the
	// document, and holding no code or imported with AllowAnyOverrides.
	// Only an admitted item is ever served.
	Admitted bool `json:"admitted"`
	// Dir is the item's directory under a recipe's bundled/ (#610): a
	// converted source collection's item, whose "container" is the
	// recipe's cache entry. Empty for an item of a kept container.
	Dir string `json:"dir,omitempty"`
}

// collectionsRoot is where game's collections are kept: beside its mod
// cache, as the Nexus recipes are (collectionCacheDir).
func (s *Service) collectionsRoot(game *domain.Game) string {
	root := filepath.Join(s.cacheDir, game.ID)
	if game.CachePath != "" {
		root = game.CachePath
	}
	return filepath.Join(root, "collections")
}

// keptBundleDir is the directory collectionID's kept containers live in.
func (s *Service) keptBundleDir(game *domain.Game, collectionID string) (string, error) {
	if !validBundleCollectionID(collectionID) {
		return "", fmt.Errorf("%w: collection id %q cannot name a kept collection", ErrInvalidBundle, collectionID)
	}
	return filepath.Join(s.collectionsRoot(game), "lmm-"+collectionID), nil
}

// keepCollectionContainer keeps data - a container whose bundle has been
// read (every item's digest known) - for collectionID, with its index:
// every item of bundle, those in admitted marked so. The same container is
// kept once; a file already there whose bytes are not data (a damaged
// copy) is replaced. An index already there keeps what it admitted: an
// admission is only ever added. Both are written beside their final names
// and renamed into place, so neither is ever half-written.
func (s *Service) keepCollectionContainer(game *domain.Game, collectionID string, data []byte, bundle *collectionBundle, names map[string]string, admitted map[string]bool) error {
	dir, err := s.keptBundleDir(game, collectionID)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	full := hex.EncodeToString(sum[:])
	base := filepath.Join(dir, full[:bundleVersionLen])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("keeping the collection's bundled files: %w", err)
	}
	if fileSHA256(base+CollectionContainerExt) != full {
		if err := writeAtomic(dir, base+CollectionContainerExt, data); err != nil {
			return fmt.Errorf("keeping the collection's bundled files: %w", err)
		}
	}
	index := keptIndex{Container: full, Items: map[string]keptItem{}}
	if old, err := readKeptIndex(base + keptIndexSuffix); err == nil && old.Container == full {
		index.Items = old.Items
	}
	for _, name := range bundle.names() {
		it := bundle.items[name]
		prev := index.Items[name]
		index.Items[name] = keptItem{
			Name: firstNonEmpty(names[name], prev.Name), Version: it.digest, Digest: it.sum,
			Bytes: it.bytes, Files: len(it.members), Admitted: prev.Admitted || admitted[name],
		}
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := writeAtomic(dir, base+keptIndexSuffix, encoded); err != nil {
		return fmt.Errorf("keeping the collection's bundled files: %w", err)
	}
	return nil
}

// writeAtomic writes data to path through a temporary file in dir.
func writeAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".keep-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// fileSHA256 is path's sha256 (hex), or "" when it is not a regular file
// that can be read.
func fileSHA256(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxCollectionContainer+1)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readKeptIndex reads a kept container's index.
func readKeptIndex(path string) (*keptIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var index keptIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

// keptEntry is one admitted item of one kept container.
type keptEntry struct {
	container string // the container's path, or a recipe's entry (item.Dir set)
	item      keptItem
}

// keepRecipeBundle keeps a converted source collection's bundled items
// (#610): their trees stay in the recipe's cache entry (b.recipeDir), and
// an index beside it - <entry>.json - records each item's digest and
// whether this import admitted it, exactly as a kept container's index
// does. An index already there keeps what it admitted.
func (s *Service) keepRecipeBundle(game *domain.Game, b *pendingBundle, admitted map[string]bool) error {
	dir := filepath.Dir(b.recipeDir)
	if filepath.Base(dir) != b.collectionID || filepath.Dir(dir) != s.collectionsRoot(game) {
		return fmt.Errorf("%w: the recipe %s is not %s's", ErrInvalidBundle, b.recipeDir, b.collectionID)
	}
	path := b.recipeDir + keptIndexSuffix
	index := keptIndex{Items: map[string]keptItem{}}
	if old, err := readKeptIndex(path); err == nil {
		index.Items = old.Items
	}
	for _, name := range b.bundle.names() {
		it := b.bundle.items[name]
		prev := index.Items[name]
		// An earlier admission holds only for the same content (#610
		// review): a recipe entry re-fetched with other files under the
		// same name is admitted by this import's own decision alone.
		was := prev.Admitted && prev.Digest == it.sum
		index.Items[name] = keptItem{
			Name: firstNonEmpty(b.names[name], prev.Name), Version: it.digest, Digest: it.sum,
			Bytes: it.bytes, Files: len(it.members), Admitted: was || admitted[name], Dir: it.display,
		}
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := writeAtomic(dir, path, encoded); err != nil {
		return fmt.Errorf("keeping the collection's bundled files: %w", err)
	}
	return nil
}

// admittedItems lists the admitted versions of modID's item the kept
// containers' indexes record, newest index first, each version once.
func (s *Service) admittedItems(game *domain.Game, modID string) (item string, out []keptEntry, err error) {
	return s.keptItems(game, modID, true)
}

// keptItems is admittedItems; admitted false lists instead the versions the
// indexes record but no import admitted (held back, or named by no entry).
func (s *Service) keptItems(game *domain.Game, modID string, admitted bool) (item string, out []keptEntry, err error) {
	collectionID, item, ok := splitBundledModID(modID)
	if !ok {
		return "", nil, fmt.Errorf("%w: %q is not a bundled item's id (<collection id>.<item>)", ErrInvalidBundle, modID)
	}
	dir, err := s.keptBundleDir(game, collectionID)
	if err != nil {
		return "", nil, err
	}
	type found struct {
		keptEntry
		mod int64
	}
	var all []found
	// The kept containers' indexes, then (#610) the indexes beside a
	// converted source collection's recipes, whose collection id is the
	// recipes' directory: <source>-<slug>/<revision>.json beside
	// <source>-<slug>/<revision>/.
	for _, place := range []struct {
		dir    string
		recipe bool
	}{{dir, false}, {filepath.Join(s.collectionsRoot(game), collectionID), true}} {
		entries, err := os.ReadDir(place.dir)
		if err != nil && !os.IsNotExist(err) {
			return "", nil, err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), keptIndexSuffix) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			index, err := readKeptIndex(filepath.Join(place.dir, e.Name()))
			if err != nil {
				continue
			}
			it, ok := index.Items[item]
			if !ok || it.Admitted != admitted || len(it.Version) != bundleVersionLen || !strings.HasPrefix(it.Digest, it.Version) || place.recipe != (it.Dir != "") {
				continue
			}
			container := filepath.Join(place.dir, strings.TrimSuffix(e.Name(), keptIndexSuffix))
			if !place.recipe {
				container += CollectionContainerExt
			}
			all = append(all, found{keptEntry{container: container, item: it}, info.ModTime().UnixNano()})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].mod > all[j].mod })
	seen := map[string]bool{}
	for _, f := range all {
		if !seen[f.item.Version] {
			seen[f.item.Version] = true
			out = append(out, f.keptEntry)
		}
	}
	return item, out, nil
}

// bundledVersions lists the admitted versions of the bundled mod modID,
// newest kept container first. None is ErrBundleNotKept: the collection was
// never imported here, its container is gone, or no import let the item in.
func (s *Service) bundledVersions(game *domain.Game, modID string) ([]bundledVersion, error) {
	item, entries, err := s.admittedItems(game, modID)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: %s (import the collection's .lmmc again; an item holding code needs --allow-any-overrides)", ErrBundleNotKept, modID)
	}
	out := make([]bundledVersion, len(entries))
	seen := map[string]bool{}
	for i, e := range entries {
		out[i] = bundledVersion{name: firstNonEmpty(e.item.Name, item), digest: e.item.Version, bytes: e.item.Bytes}
		seen[e.item.Version] = true
	}
	// A version kept but never admitted is listed too, held back: a
	// profile pinning one then fails at the fetch, which says to import the
	// collection again, rather than at a selection that calls the version
	// gone upstream. It is never primary, and the fetch refuses it.
	_, held, err := s.keptItems(game, modID, false)
	if err != nil {
		return nil, err
	}
	for _, e := range held {
		if !seen[e.item.Version] {
			seen[e.item.Version] = true
			out = append(out, bundledVersion{name: firstNonEmpty(e.item.Name, item), digest: e.item.Version, bytes: e.item.Bytes, heldBack: true})
		}
	}
	return out, nil
}

// findBundledItem is the bundled mod modID's tree at version, from the
// kept container whose index admitted it: only that item is read and
// hashed, and it must hash to the whole digest the index recorded.
func (s *Service) findBundledItem(game *domain.Game, modID, version string) (*bundledItem, error) {
	item, entries, err := s.admittedItems(game, modID)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.item.Version != version {
			continue
		}
		if e.item.Dir != "" {
			// #610: a converted source collection's item, read from its
			// recipe's entry under the same rules it was imported by.
			rb := readRecipeBundleAt(e.container, e.item.Dir)
			if rb.item == nil {
				return nil, fmt.Errorf("%w: the kept copy of %s's collection is damaged (%s); import the collection again", ErrBundleNotKept, modID, rb.problem)
			}
			if rb.item.sum != e.item.Digest {
				return nil, fmt.Errorf("%w: the kept copy of %s does not match what was imported; import the collection again", ErrBundleNotKept, modID)
			}
			rb.item.name, rb.item.display = item, e.item.Name
			return rb.item, nil
		}
		bundle, err := readKeptContainer(e.container)
		if err != nil {
			return nil, fmt.Errorf("%w: the kept copy of %s's collection is damaged (%v); import the collection's .lmmc again", ErrBundleNotKept, modID, err)
		}
		it := bundle.item(item)
		if it == nil {
			return nil, fmt.Errorf("%w: the kept copy of %s's collection no longer holds it; import the collection's .lmmc again", ErrBundleNotKept, modID)
		}
		if err := it.read(); err != nil {
			return nil, err
		}
		if it.sum != e.item.Digest {
			return nil, fmt.Errorf("%w: the kept copy of %s does not match what was imported; import the collection's .lmmc again", ErrBundleNotKept, modID)
		}
		it.display = e.item.Name
		return it, nil
	}
	return nil, fmt.Errorf("%w: %s at version %s (import the collection's .lmmc again; an item holding code needs --allow-any-overrides)", ErrBundleNotKept, modID, version)
}

// readKeptContainer reads one kept container's bundle under the reader's
// own rules - a kept file is read as warily as the one it was kept from -
// without reading its items: a rebuild reads only the one it needs.
func readKeptContainer(path string) (*collectionBundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxCollectionContainer+1))
	if err != nil {
		return nil, err
	}
	_, bundle, _, err := parseCollectionContainer(data, false)
	if err == nil && bundle == nil {
		err = fmt.Errorf("%w: it holds no bundled files", ErrInvalidBundle)
	}
	return bundle, err
}
