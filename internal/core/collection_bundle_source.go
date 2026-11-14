// collection_bundle_source.go is the lmm-collection synthetic source
// (#623; design §6, the lmm-merged precedent): the source a bundled
// collection item is fetched from. It has no remote. Its "downloads" are
// the `.lmmc` containers an import keeps (collection_bundle_keep.go), and a
// fetch extracts one item's files/<item>/ tree from the kept container
// whose index ADMITTED it at the version asked for - never an item the
// import held back or no entry named.
//
// Because it answers GetMod, GetModFiles and Fetch like any source, every
// flow that installs a mod from a profile - import, re-plans, profile
// switch and apply, verify --fix's redownload - rebuilds a bundled item's
// cache entry the same way, from the kept container. It is never
// registered: GetSource, ListSources, search and a game's source map never
// see it, and Service.sourceByID serves it, by its id alone, to the
// install paths only. It checks no updates; a bundled item changes only
// when the collection is imported again.
//
// A Nexus collection's bundled/ tree (C5, #610) maps onto the same source:
// a converted bundle item is an lmm-collection mod whose collection id
// names the recipe's cache entry, kept with an index the same way.
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

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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
// never registered (so never listed or mappable), or a registered one. Only
// the install paths (GetMod, GetModFiles, the acquire, an import plan's
// availability) use it; GetSource stays registry-only (#623 review B2).
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
// item the kept containers hold, admitted ones first and newest first - its
// id and version are the version's digest. A version no import admitted is
// listed as HELD_BACK, never primary, and Fetch refuses it.
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
		if v.heldBack {
			files[i].IsPrimary, files[i].Category = false, "HELD_BACK"
			files[i].Description = "held back by its import: import the collection's .lmmc again (with --allow-any-overrides, for an item holding code)"
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
	// heldBack marks a version kept but never admitted: listed, never
	// fetched.
	heldBack bool
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
	if got := bundleDigest(item.members, sums); got != item.sum {
		return fmt.Errorf("%w: files/%s/ unpacked as %s, not the %s it was read as", ErrInvalidBundle, item.name, got, item.sum)
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
