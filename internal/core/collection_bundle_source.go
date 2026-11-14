// collection_bundle_source.go is the lmm-collection synthetic source
// (#623; design §6, the lmm-merged precedent): the source a bundled
// collection item is fetched from. It has no remote. Its "downloads" are
// the `.lmmc` containers an import keeps, beside the Nexus recipes, under
// <cache root>/collections/lmm-<collection id>/<container digest>.lmmc, and
// a fetch extracts one item's files/<item>/ tree from the kept container
// whose copy of it hashes to the version asked for.
//
// Because it answers GetMod, GetModFiles and Fetch like any source, every
// flow that installs a mod from a profile - import, re-plans, profile
// switch and apply, verify --fix's redownload - rebuilds a bundled item's
// cache entry the same way, from the kept container. It is never listed
// (ListSources, search, `lmm source list`): Service.sourceByID serves it
// by its id alone. It checks no updates; a bundled item changes only when
// the collection is imported again.
//
// A Nexus collection's bundled/ tree (C5, #610) maps onto the same source:
// a converted bundle item is an lmm-collection mod whose collection id
// names the recipe's cache entry, and findBundledItem gains that entry as
// a second place to look (see the #623 report).
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// bundledSource is the lmm-collection source.
type bundledSource struct {
	s *Service
}

// ErrBundleNotKept is a bundled item no kept container holds at the
// version asked for: the collection was never imported here, or its
// container was removed. Importing the collection's `.lmmc` again keeps it.
var ErrBundleNotKept = errors.New("no collection kept on this machine holds this bundled item")

// sourceByID is the source with id: the lmm-collection source, which is
// never registered (so never listed), or a registered one.
func (s *Service) sourceByID(id string) (source.ModSource, error) {
	if id == domain.SourceCollection {
		return &bundledSource{s: s}, nil
	}
	return s.registry.Get(id)
}

// ID implements source.ModSource.
func (b *bundledSource) ID() string { return domain.SourceCollection }

// Name implements source.ModSource.
func (b *bundledSource) Name() string { return "Collection files" }

// AuthURL implements source.ModSource: there is nothing to log in to.
func (b *bundledSource) AuthURL() string { return "" }

// ExchangeToken implements source.ModSource.
func (b *bundledSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

// Search implements source.ModSource: bundled items are not searchable.
func (b *bundledSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}

// GetDependencies implements source.ModSource: a bundled item has none.
func (b *bundledSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

// GetDownloadURL implements source.ModSource: there is no URL, so core
// takes the Fetch path.
func (b *bundledSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

// CheckUpdates implements source.ModSource: a bundled item has no remote
// to have a newer version.
func (b *bundledSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// Capabilities implements source.CapabilityReporter: none.
func (b *bundledSource) Capabilities() source.Capabilities { return source.Capabilities{} }

// gameFor is the game a call is for: the one in ctx, else gameID's.
func (b *bundledSource) gameFor(ctx context.Context, gameID string) (*domain.Game, error) {
	if g := source.GameFromContext(ctx); g != nil {
		return g, nil
	}
	if g, ok := b.s.game(gameID); ok {
		return g, nil
	}
	return nil, fmt.Errorf("game not found: %s", gameID)
}

// GetMod implements source.ModSource: the item, at the version of the
// newest kept container that holds it.
func (b *bundledSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	game, err := b.gameFor(ctx, gameID)
	if err != nil {
		return nil, err
	}
	versions, err := b.s.bundledVersions(game, modID)
	if err != nil {
		return nil, err
	}
	return &domain.Mod{
		ID: modID, SourceID: domain.SourceCollection, Name: versions[0].name, Version: versions[0].digest,
		GameID: gameID, Summary: "Bundled in a collection by its author",
	}, nil
}

// GetModFiles implements source.ModSource: one file per version of the
// item the kept containers hold, newest first - its id and version are the
// version's digest.
func (b *bundledSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	game, err := b.gameFor(ctx, mod.GameID)
	if err != nil {
		return nil, err
	}
	versions, err := b.s.bundledVersions(game, mod.ID)
	if err != nil {
		return nil, err
	}
	files := make([]domain.DownloadableFile, len(versions))
	for i, v := range versions {
		files[i] = domain.DownloadableFile{
			ID: v.digest, Name: v.name, FileName: v.name, Version: v.digest, Size: v.bytes,
			IsPrimary: true, Category: "MAIN",
		}
	}
	return files, nil
}

// Fetch implements source.Fetcher: it writes the item's files/<item>/
// tree, at the version fileID names, into destDir/<item>/ and returns that
// directory, which core then ingests like a directory source's.
func (b *bundledSource) Fetch(ctx context.Context, mod *domain.Mod, fileID, destDir string, progress source.FetchProgressFunc) (string, error) {
	game, err := b.gameFor(ctx, mod.GameID)
	if err != nil {
		return "", err
	}
	item, err := b.s.findBundledItem(game, mod.ID, fileID)
	if err != nil {
		return "", err
	}
	progress(source.FetchPhaseStarted, "Unpacking "+item.name+" from its collection", item.bytes)
	dir := filepath.Join(destDir, item.name)
	if err := extractBundledItem(ctx, item, dir); err != nil {
		return "", err
	}
	progress(source.FetchPhaseDone, "Unpacked "+item.name, item.bytes)
	return dir, nil
}

// bundledVersion is one version of a bundled item a kept container holds.
type bundledVersion struct {
	name   string
	digest string
	bytes  int64
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

// keepCollectionContainer keeps a container with bundled files for
// collectionID, under the first 16 hex digits of its sha256, so the same
// container is kept once. It is written beside its final name and renamed
// into place: a kept container is never half-written.
func (s *Service) keepCollectionContainer(game *domain.Game, collectionID string, data []byte) error {
	dir, err := s.keptBundleDir(game, collectionID)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	final := filepath.Join(dir, hex.EncodeToString(sum[:])[:bundleDigestLen]+CollectionContainerExt)
	if info, err := os.Lstat(final); err == nil && info.Mode().IsRegular() && info.Size() == int64(len(data)) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("keeping the collection's bundled files: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".keep-*")
	if err != nil {
		return fmt.Errorf("keeping the collection's bundled files: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), final)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("keeping the collection's bundled files: %w", werr)
	}
	return nil
}

// keptContainers lists collectionID's kept containers, newest first.
func (s *Service) keptContainers(game *domain.Game, collectionID string) ([]string, error) {
	dir, err := s.keptBundleDir(game, collectionID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	type kept struct {
		path string
		mod  int64
	}
	var out []kept
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), CollectionContainerExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, kept{path: filepath.Join(dir, e.Name()), mod: info.ModTime().UnixNano()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mod > out[j].mod })
	paths := make([]string, len(out))
	for i, k := range out {
		paths[i] = k.path
	}
	return paths, nil
}

// readKeptContainerDoc reads one kept container's document and bundle,
// under the reader's own rules: a kept file is read as warily as the one it
// was kept from.
func readKeptContainerDoc(path string) ([]byte, *collectionBundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxCollectionContainer+1))
	if err != nil {
		return nil, nil, err
	}
	doc, bundle, _, err := readCollectionContainer(data)
	return doc, bundle, err
}

// bundledItemName is the display name doc gives its bundled item, or "".
func bundledItemName(doc []byte, item string) string {
	parsed, err := config.ParseCollection(doc)
	if err != nil {
		return ""
	}
	for _, it := range parsed.Items {
		if it.Ref.SourceID == domain.SourceCollection && it.Ref.ModID == item {
			return domain.SanitizeLine(it.Name)
		}
	}
	return ""
}

// bundledVersions lists the versions of the bundled mod modID the kept
// containers hold, newest container first, each once.
func (s *Service) bundledVersions(game *domain.Game, modID string) ([]bundledVersion, error) {
	collectionID, item, ok := splitBundledModID(modID)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a bundled item's id (<collection id>.<item>)", ErrInvalidBundle, modID)
	}
	paths, err := s.keptContainers(game, collectionID)
	if err != nil {
		return nil, err
	}
	var out []bundledVersion
	seen := map[string]bool{}
	for _, path := range paths {
		doc, bundle, err := readKeptContainerDoc(path)
		if err != nil || bundle == nil {
			continue
		}
		if it := bundle.item(item); it != nil && !seen[it.digest] {
			seen[it.digest] = true
			name := bundledItemName(doc, item)
			if name == "" {
				name = item
			}
			out = append(out, bundledVersion{name: name, digest: it.digest, bytes: it.bytes})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s (import the collection's .lmmc again)", ErrBundleNotKept, modID)
	}
	return out, nil
}

// findBundledItem is the bundled mod modID's tree at version digest, from
// the kept container that holds it.
func (s *Service) findBundledItem(game *domain.Game, modID, digest string) (*bundledItem, error) {
	collectionID, item, ok := splitBundledModID(modID)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a bundled item's id (<collection id>.<item>)", ErrInvalidBundle, modID)
	}
	paths, err := s.keptContainers(game, collectionID)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		doc, bundle, err := readKeptContainerDoc(path)
		if err != nil || bundle == nil {
			continue
		}
		if it := bundle.item(item); it != nil && it.digest == digest {
			it.display = bundledItemName(doc, item)
			return it, nil
		}
	}
	return nil, fmt.Errorf("%w: %s at version %s (import the collection's .lmmc again)", ErrBundleNotKept, modID, digest)
}

// extractBundledItem writes item's files under dir, which must not exist:
// each a new regular file (0644: never executable), each read through a
// limit its size must match, and the whole re-hashed to item's digest as
// it is written - what lands on disk is what the version names.
func extractBundledItem(ctx context.Context, item *bundledItem, dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("unpacking %s: %w", item.name, err)
	}
	sums := make([]string, len(item.members))
	for i, m := range item.members {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(m.rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("unpacking %s: %w", item.name, err)
		}
		sum, err := writeBundleMember(m, target)
		if err != nil {
			return fmt.Errorf("unpacking %s/%s: %w", item.name, m.rel, err)
		}
		sums[i] = sum
	}
	if got := bundleDigest(item.members, sums); got != item.digest {
		return fmt.Errorf("%w: files/%s/ unpacked as %s, not the %s it was read as", ErrInvalidBundle, item.name, got, item.digest)
	}
	return nil
}

// writeBundleMember writes m to target, a new file, and returns its
// sha256.
func writeBundleMember(m bundleMember, target string) (string, error) {
	rc, err := m.open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, m.size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n != m.size {
		return "", fmt.Errorf("its content is %d bytes, not %d", n, m.size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
