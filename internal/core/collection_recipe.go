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
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

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
	in.modTypeSupported = s.modTypeRoutes(game)
	// A recipe's plugin order is replayed only where lmm writes the game's
	// plugin list (#611): anywhere else it would be saved and never applied.
	_, in.pluginOrder = s.pluginRules(game)
	// #620: a recipe's file load order is replayed only where the game's
	// adapter writes it.
	in.loadOrders = s.loadOrderSupport(game)
	in.withOptional = opts.WithOptional
	if len(opts.Optional) > 0 {
		in.optional = make(map[string]bool, len(opts.Optional))
		for _, id := range opts.Optional {
			in.optional[id] = true
		}
	}

	dir, err := s.fetchCollectionRecipe(ctx, game, sourceID, fetcher, list)
	if err == nil {
		// Read through an os.Root: nothing the parser opens can leave the
		// entry, whatever is in it.
		var root *os.Root
		if root, err = os.OpenRoot(dir); err == nil {
			var m source.CollectionManifest
			if m, err = fetcher.ParseManifest(root.FS()); err == nil {
				err = unreadableLoadOrder(&m, in.loadOrders)
			}
			if err == nil {
				in.manifest = &m
			}
			_ = root.Close()
		}
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		in.manifestErr = recipeErrorText(err)
		s.logger().Debug("a collection recipe could not be read", "source", sourceID, "collection", list.ID, "err", err)
	}
	return convertCollection(in)
}

// modTypeRouted reports whether game's adapter knows where a mod of the
// stated type deploys, and that place exists for the game: a game-root
// route needs the game's install_path.
func modTypeRouted(a adapter.GameAdapter, game *domain.Game, modType string) bool {
	root, ok := adapter.RootForModType(a, game, modType)
	return ok && (root != adapter.RootInstallPath || game.InstallPath != "")
}

// modTypeRoutes is the importing game's answer to "does a stated mod type
// deploy here?" (#607): its adapter's RootForModType, and for a game-root
// route an install_path to deploy under. nil when the game's adapter cannot
// be resolved, which routes no type.
func (s *Service) modTypeRoutes(game *domain.Game) func(modType string) bool {
	a, err := s.AdapterFor(game)
	if err != nil {
		return nil
	}
	return func(t string) bool { return modTypeRouted(a, game, t) }
}

// unreadableLoadOrder is the error for a recipe whose file load order the
// game writes has entries that could not be read (#620 review): that order
// is the recipe's to replay, so the recipe is unreadable, as a parse error
// makes it. An order the game does not write is refused by its feature
// whatever its entries hold, so its unreadable entries change nothing.
func unreadableLoadOrder(m *source.CollectionManifest, supported func(id string) bool) error {
	for _, lo := range m.FileLoadOrders {
		if lo.Invalid != "" && supported != nil && supported(lo.ID) {
			return fmt.Errorf("%w: the %s load order: %s", source.ErrInvalidManifest, lo.ID, lo.Invalid)
		}
	}
	return nil
}

// loadOrderSupport reports which file load orders game's adapter writes
// (#620), by id; nil when it writes none.
func (s *Service) loadOrderSupport(game *domain.Game) func(id string) bool {
	specs := s.loadOrderSpecs(game)
	if len(specs) == 0 {
		return nil
	}
	return func(id string) bool {
		return slices.ContainsFunc(specs, func(spec adapter.FileLoadOrderSpec) bool { return spec.ID == id })
	}
}

// recipeErrorText words why a recipe could not be read, for a plan: a
// plain sentence, its text cleaned (domain.SanitizeText) and capped, since
// a parser's error can quote the recipe's own strings.
func recipeErrorText(err error) string {
	msg := recipeErrorSentence(err)
	msg = domain.SanitizeText(msg)
	if len(msg) > maxCollectionLine {
		msg = strings.ToValidUTF8(msg[:maxCollectionLine], "") + "…"
	}
	return msg
}

// recipeErrorSentence is recipeErrorText before it is cleaned.
func recipeErrorSentence(err error) string {
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
	if !safeCollectionSegment.MatchString(sourceID) || !safeCollectionSegment.MatchString(slug) || revision <= 0 {
		return "", fmt.Errorf("collection %q on %q cannot name a cache entry", slug, sourceID)
	}
	root := filepath.Join(s.cacheDir, game.ID)
	if game.CachePath != "" {
		root = game.CachePath
	}
	return filepath.Join(root, "collections", sourceID+"-"+slug, strconv.Itoa(revision)), nil
}

// fetchCollectionRecipe returns the directory holding list's recipe
// manifest: the cache entry when it is already there, else downloaded,
// checked, and its collection.json alone extracted into staging and moved
// into place whole, so an entry is never half-written.
//
// The archive is a stranger's file, so (#607 review 2, 3):
//
//   - the download is capped as it is written (maxCollectionRecipeBytes);
//   - the archive is listed before anything is extracted, and refused
//     unless it holds exactly one top-level collection.json that is a
//     regular file within the manifest cap - a highly compressed bomb, a
//     directory or a symlink named collection.json is never extracted;
//   - only that one member is extracted (bundled/ and patches/ wait for
//     C5, which needs them), and the extracted tree is walked without
//     following links: anything but a regular file or a directory refuses;
//   - a cached entry is trusted only as a regular collection.json within
//     the cap; anything else is removed and fetched again.
//
// The archive's signed link is a credential, so no error returned here
// contains it.
func (s *Service) fetchCollectionRecipe(ctx context.Context, game *domain.Game, sourceID string, fetcher source.CollectionManifestFetcher, list source.Collection) (string, error) {
	dir, err := s.collectionCacheDir(game, sourceID, list.ID, list.Revision)
	if err != nil {
		return "", err
	}
	if err := checkRecipeTree(dir); err == nil {
		return dir, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Not what lmm writes: fetch it again rather than read it.
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return "", fmt.Errorf("replacing the recipe cache entry: %w", rmErr)
		}
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
	if err := s.downloadRecipe(ctx, link, archive); err != nil {
		return "", redactLink(fmt.Errorf("downloading the recipe: %w", err), link)
	}
	if err := s.checkRecipeListing(ctx, archive); err != nil {
		return "", err
	}
	tree := filepath.Join(staging, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		return "", err
	}
	if err := s.extractRecipeManifest(ctx, archive, tree); err != nil {
		return "", err
	}
	if err := checkRecipeTree(tree); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if err := renameRecipe(tree, dir); err != nil {
		// Another plan of the same revision may have put it there first.
		if checkRecipeTree(dir) == nil {
			return dir, nil
		}
		if !errors.Is(err, syscall.EXDEV) {
			return "", fmt.Errorf("keeping the recipe: %w", err)
		}
		// A cache_path on another filesystem than the data directory's
		// staging: copy into a sibling of the entry, then rename there.
		tmp, mkErr := os.MkdirTemp(filepath.Dir(dir), ".recipe-*")
		if mkErr != nil {
			return "", fmt.Errorf("keeping the recipe: %w", mkErr)
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if err := copyDir(ctx, tree, tmp); err != nil {
			return "", fmt.Errorf("keeping the recipe: %w", err)
		}
		if err := os.Rename(tmp, dir); err != nil {
			if checkRecipeTree(dir) == nil {
				return dir, nil
			}
			return "", fmt.Errorf("keeping the recipe: %w", err)
		}
	}
	return dir, nil
}

// errRecipeRedirectNotHTTPS refuses a recipe link that redirects off https.
var errRecipeRedirectNotHTTPS = errors.New("the recipe's link redirected to a plain-http address, which lmm does not follow")

// renameRecipe moves an extracted recipe into the cache; a seam for the
// cross-device test.
var renameRecipe = os.Rename

// maxCollectionManifestBytes caps a recipe's collection.json: the largest
// surveyed (2,444 items) is about 2 MB.
const maxCollectionManifestBytes = 32 << 20

// downloadRecipe GETs link into dest, refusing a body over
// maxCollectionRecipeBytes as it arrives rather than after it is written.
func (s *Service) downloadRecipe(ctx context.Context, link, dest string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	// The recipe's link is signed, and so is any link it redirects to:
	// never follow one onto plain http, where it would cross the network
	// in the clear.
	client := *s.downloader.redirectSafeClient(nil)
	base := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" {
			return errRecipeRedirectNotHTTPS
		}
		if base != nil {
			return base(next, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error's text carries the URL that failed - after a redirect,
		// a signed link the caller never saw, so redactLink cannot cut it.
		// Report it without its query, as downloadOnce does.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			reportURL := "the recipe's link"
			if parsed, perr := url.Parse(uerr.URL); perr == nil {
				parsed.RawQuery, parsed.Fragment, parsed.User = "", "", nil
				reportURL = parsed.String()
			}
			return fmt.Errorf("requesting %s: %w", reportURL, uerr.Err)
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error: %s", statusText(resp))
	}
	if resp.ContentLength > maxCollectionRecipeBytes {
		return fmt.Errorf("the recipe archive is %d bytes, over the %d-byte limit", resp.ContentLength, maxCollectionRecipeBytes)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxCollectionRecipeBytes+1))
	if err != nil {
		return err
	}
	if n > maxCollectionRecipeBytes {
		return fmt.Errorf("the recipe archive is over the %d-byte limit", maxCollectionRecipeBytes)
	}
	return nil
}

// checkRecipeListing lists archive (7z l -slt) and refuses it unless it
// holds exactly one top-level collection.json that is a regular file within
// maxCollectionManifestBytes - checked BEFORE anything is extracted, so a
// highly compressed member is never inflated.
func (s *Service) checkRecipeListing(ctx context.Context, archive string) error {
	out, err := exec.CommandContext(ctx, "7z", "l", "-slt", "--", archive).Output()
	if err != nil {
		s.logger().Debug("listing a collection recipe failed", "err", err, "output", string(out))
		return fmt.Errorf("%w: the recipe archive could not be listed (7z could not read it)", source.ErrInvalidManifest)
	}
	return checkRecipeListingText(string(out))
}

// checkRecipeListingText is checkRecipeListing over `7z l -slt`'s output.
// The archive must be a 7z (every surveyed recipe is: VERIFIED), so a zip,
// a tar or anything else 7z happens to read is refused before any of its
// fields - a zip comment, a tar link - can matter; and the one
// collection.json must be a regular file with no link of either kind.
func checkRecipeListingText(out string) error {
	header, body, ok := strings.Cut(out, "\n----------\n")
	if !ok {
		return fmt.Errorf("%w: the recipe archive's listing is not 7z's", source.ErrInvalidManifest)
	}
	var is7z bool
	for line := range strings.Lines(header) {
		if strings.TrimRight(line, "\r\n") == "Type = 7z" {
			is7z = true
		}
	}
	if !is7z {
		return fmt.Errorf("%w: the recipe archive is not a 7z archive", source.ErrInvalidManifest)
	}
	var found int
	for block := range strings.SplitSeq(body, "\n\n") {
		fields := map[string]string{}
		for line := range strings.Lines(block) {
			if k, v, ok := strings.Cut(strings.TrimRight(line, "\r\n"), " = "); ok {
				fields[k] = v
			}
		}
		if fields["Path"] != collectionRecipeManifest {
			continue
		}
		found++
		mode := ""
		if attrs := strings.Fields(fields["Attributes"]); len(attrs) > 1 {
			mode = attrs[1]
		}
		if fields["Folder"] == "+" || strings.HasPrefix(mode, "d") || strings.HasPrefix(mode, "l") || strings.Contains(fields["Attributes"], "D") ||
			fields["Symbolic Link"] != "" || fields["Hard Link"] != "" {
			return fmt.Errorf("%w: the recipe's %s is not a regular file", source.ErrInvalidManifest, collectionRecipeManifest)
		}
		size, err := strconv.ParseInt(fields["Size"], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("%w: the recipe's %s has no size", source.ErrInvalidManifest, collectionRecipeManifest)
		}
		if size > maxCollectionManifestBytes {
			return fmt.Errorf("%w: the recipe's %s is %d bytes, over the %d-byte limit", source.ErrInvalidManifest, collectionRecipeManifest, size, maxCollectionManifestBytes)
		}
	}
	switch found {
	case 0:
		return fmt.Errorf("%w: the recipe archive holds no %s", source.ErrInvalidManifest, collectionRecipeManifest)
	case 1:
		return nil
	}
	return fmt.Errorf("%w: the recipe archive holds %d entries named %s", source.ErrInvalidManifest, found, collectionRecipeManifest)
}

// extractRecipeManifest extracts archive's top-level collection.json, and
// nothing else, into dest. 7z's own output names local paths and can carry
// the archive's text, so it goes to the debug log, never into the message.
func (s *Service) extractRecipeManifest(ctx context.Context, archive, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, extract7zTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "7z", "x", "-y", "-r-", "-o"+dest, "--", archive, collectionRecipeManifest).CombinedOutput()
	if err != nil {
		s.logger().Debug("extracting a collection recipe failed", "err", err, "output", string(out))
		return fmt.Errorf("%w: 7z could not extract the recipe's %s", source.ErrInvalidManifest, collectionRecipeManifest)
	}
	return nil
}

// checkRecipeTree checks an extracted (or cached) recipe entry without
// following any link: a regular collection.json within the cap, and
// nothing in the tree but regular files and directories. A missing entry
// is fs.ErrNotExist.
func checkRecipeTree(dir string) error {
	info, err := os.Lstat(filepath.Join(dir, collectionRecipeManifest))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: the recipe's %s is not a regular file", source.ErrInvalidManifest, collectionRecipeManifest)
	}
	if info.Size() > maxCollectionManifestBytes {
		return fmt.Errorf("%w: the recipe's %s is over the %d-byte limit", source.ErrInvalidManifest, collectionRecipeManifest, maxCollectionManifestBytes)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
		return fmt.Errorf("%w: the recipe's %s is a hard link", source.ErrInvalidManifest, collectionRecipeManifest)
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() && !d.IsDir() {
			return fmt.Errorf("%w: the recipe holds %s, which is not a regular file or a directory", source.ErrInvalidManifest, d.Name())
		}
		return nil
	})
}

// redactedError is an error whose text had a signed link cut out of it;
// it still unwraps to the original, for errors.Is.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactLink removes link - the whole URL, as written or as an error
// quotes it - from err's text. Only the URL is cut: its query values are
// short and could occur anywhere in a message.
func redactLink(err error, link string) error {
	if err == nil || link == "" {
		return err
	}
	msg := err.Error()
	for _, c := range []string{link, strconv.Quote(link)[1 : len(strconv.Quote(link))-1]} {
		msg = strings.ReplaceAll(msg, c, "<signed link>")
	}
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, err: err}
}
