// collection_recipe.go resolves a source's collection and fetches its
// recipe (#607 C2; design docs/plans/2026-11-10-collections-design.md §2):
// the list from the source's GameCollectionResolver, the recipe archive
// from its CollectionManifestFetcher's link, downloaded and extracted by
// core into a cache entry of its own, parsed by the source, and converted
// (collection_convert.go) into lmm's collection document. Core never
// imports a concrete source.
package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// CollectionConvertOptions says what of a collection to convert.
type CollectionConvertOptions struct {
	// WithOptional opts every optional item in. Optional items are off by
	// default (owner ruling, 2026-11-10).
	WithOptional bool
	// Optional opts in the optional items it names, by
	// ConvertedCollectionItem.ID.
	Optional []string
}

// collectionRecipeManifest is the recipe's manifest at the archive root.
const collectionRecipeManifest = "collection.json"

// maxCollectionRecipeBytes caps a recipe archive's download. The largest
// surveyed recipe (2,444 items) is 650 KB; a bundling collection's is
// larger, and still nowhere near this.
const maxCollectionRecipeBytes = 1 << 30

// safeCollectionSegment is a source id or slug fit to be one cache path
// segment.
var safeCollectionSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ConvertSourceCollection resolves ref (what the user typed) as a
// collection on the source sourceID for game, fetches and reads its recipe,
// and converts it into lmm's collection document (collection_convert.go).
// Nothing is written but the recipe's own cache entry
// (<cache>/<game>/collections/<source>-<slug>/<revision>/), which is kept:
// a re-plan of the same revision is free, and a later story installs
// bundled files from it.
//
// A recipe that cannot be fetched or read is not an error: the conversion
// carries the list alone, its verdict is manifest_unavailable and
// ManifestError says why, so a plan still shows what the collection holds.
// A reference the source cannot resolve is (IsBadCollectionRef), as is a
// source without revisioned collections.
func (s *Service) ConvertSourceCollection(ctx context.Context, game *domain.Game, sourceID, ref string, opts CollectionConvertOptions) (*ConvertedCollection, error) {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return nil, err
	}
	resolver, ok := src.(source.GameCollectionResolver)
	fetcher, ok2 := src.(source.CollectionManifestFetcher)
	if !ok || !ok2 {
		return nil, fmt.Errorf("%s has no collections lmm can import: %w", s.sourceDisplayName(sourceID), source.ErrNotSupported)
	}
	gameSourceID := game.SourceIDs[sourceID]
	if gameSourceID == "" {
		return nil, fmt.Errorf("game %s has no %s id (games.yaml source_ids: %s), so lmm cannot look up its collections there",
			game.ID, sourceID, sourceID)
	}
	list, err := resolver.ResolveGameCollection(ctx, gameSourceID, ref)
	if err != nil {
		return nil, err
	}

	in := convertInput{
		sourceID: sourceID,
		gameID:   game.ID,
		game:     s.portableGame(game),
		list:     list,
	}
	if a, err := s.AdapterFor(game); err == nil {
		in.modTypeSupported = func(t string) bool { return modTypeRouted(a, game, t) }
	}
	in.withOptional = opts.WithOptional
	if len(opts.Optional) > 0 {
		in.optional = make(map[string]bool, len(opts.Optional))
		for _, id := range opts.Optional {
			in.optional[id] = true
		}
	}

	dir, err := s.fetchCollectionRecipe(ctx, game, sourceID, fetcher, list)
	if err == nil {
		var m source.CollectionManifest
		if m, err = fetcher.ParseManifest(os.DirFS(dir)); err == nil {
			in.manifest = &m
		}
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		in.manifestErr = recipeErrorText(err)
	}
	return convertCollection(in), nil
}

// modTypeRouted reports whether game's adapter knows where a mod of the
// stated type deploys, and that place exists for the game: a game-root
// route needs the game's install_path.
func modTypeRouted(a adapter.GameAdapter, game *domain.Game, modType string) bool {
	root, ok := adapter.RootForModType(a, game, modType)
	return ok && (root != adapter.RootInstallPath || game.InstallPath != "")
}

// recipeErrorText words why a recipe could not be read, for a plan.
func recipeErrorText(err error) string {
	switch {
	case errors.Is(err, ErrSevenZipMissing):
		return "a collection's recipe is a .7z archive, and lmm reads those with the system 7z command, which is not installed: install 7zip (or p7zip) and plan again"
	case errors.Is(err, domain.ErrAuthRequired):
		return "the collection's recipe is served only to a signed-in account (a free one is enough): sign in with `lmm auth login` and plan again; " + err.Error()
	}
	return "the collection's recipe could not be read: " + err.Error()
}

// collectionCacheDir is where a collection revision's recipe is kept: the
// game's cache root (its cache_path, or the global cache's game directory,
// as mod entries are), then collections/<source>-<slug>/<revision>/.
func (s *Service) collectionCacheDir(game *domain.Game, sourceID, slug string, revision int) (string, error) {
	if !safeCollectionSegment.MatchString(sourceID) || !safeCollectionSegment.MatchString(slug) || revision < 0 {
		return "", fmt.Errorf("collection %q on %q cannot name a cache entry", slug, sourceID)
	}
	root := filepath.Join(s.cacheDir, game.ID)
	if game.CachePath != "" {
		root = game.CachePath
	}
	return filepath.Join(root, "collections", sourceID+"-"+slug, strconv.Itoa(revision)), nil
}

// fetchCollectionRecipe returns the directory holding list's extracted
// recipe: the cache entry when it is already there, else downloaded and
// extracted into staging and moved into place whole, so an entry is never
// half-written. The archive's signed link is a credential, so no error
// returned here contains it.
func (s *Service) fetchCollectionRecipe(ctx context.Context, game *domain.Game, sourceID string, fetcher source.CollectionManifestFetcher, list source.Collection) (string, error) {
	dir, err := s.collectionCacheDir(game, sourceID, list.ID, list.Revision)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(filepath.Join(dir, collectionRecipeManifest)); err == nil && info.Mode().IsRegular() {
		return dir, nil
	}
	// Every recipe is a .7z (VERIFIED across the survey): say so before
	// downloading anything.
	if _, err := exec.LookPath("7z"); err != nil {
		return "", ErrSevenZipMissing
	}
	link, err := fetcher.ManifestArchiveURL(ctx, list)
	if err != nil {
		return "", err
	}
	staging, err := newStagingDir(s.stagingRoot(), "collection-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	archive := filepath.Join(staging, "recipe.7z")
	result, err := s.downloader.Download(ctx, link, archive, nil)
	if err != nil {
		return "", redactLink(fmt.Errorf("downloading the recipe: %w", err), link)
	}
	if result != nil && result.Size > maxCollectionRecipeBytes {
		return "", fmt.Errorf("the recipe archive is %d bytes, over the %d-byte limit", result.Size, maxCollectionRecipeBytes)
	}
	tree := filepath.Join(staging, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		return "", err
	}
	if err := s.extractor.Extract(ctx, archive, tree); err != nil {
		return "", redactLink(fmt.Errorf("extracting the recipe: %w", err), link)
	}
	if info, err := os.Stat(filepath.Join(tree, collectionRecipeManifest)); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: the recipe archive holds no %s", source.ErrInvalidManifest, collectionRecipeManifest)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tree, dir); err != nil {
		// Another plan of the same revision may have put it there first.
		if info, statErr := os.Stat(filepath.Join(dir, collectionRecipeManifest)); statErr == nil && info.Mode().IsRegular() {
			return dir, nil
		}
		return "", fmt.Errorf("keeping the recipe: %w", err)
	}
	return dir, nil
}

// redactedError is an error whose text had a signed link cut out of it;
// it still unwraps to the original, for errors.Is.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactLink removes link - whole, or its query string, in any encoding Go
// errors print it in - from err's text.
func redactLink(err error, link string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	cut := []string{link}
	if u, perr := url.Parse(link); perr == nil && u.RawQuery != "" {
		cut = append(cut, u.RawQuery, url.QueryEscape(u.RawQuery))
		for _, vs := range u.Query() {
			cut = append(cut, vs...)
		}
	}
	for _, c := range cut {
		if c != "" {
			msg = strings.ReplaceAll(msg, c, "<signed link>")
		}
	}
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, err: err}
}
