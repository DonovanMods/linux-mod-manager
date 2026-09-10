// Package steamworkshop: this file is the ModSource itself - identity,
// capabilities, construction, and the local-scan capability core adopts
// from. The remote half lives in client.go / metacache.go, and the
// update-check rules in updates.go.
package steamworkshop

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// sourceID is the registry id a game maps to in games.yaml's sources block;
// the per-source game id under it IS the decimal Steam app id.
const sourceID = "steamworkshop"

// Options configures New. Every field has a working zero value except
// CacheDir, without which metadata is simply never cached (correct, just
// slower and chattier towards Valve).
type Options struct {
	// HTTPClient is the transport for Valve's Web API. nil uses
	// http.DefaultClient, as every other source does.
	HTTPClient *http.Client
	// CacheDir is lmm's cache root (app.Paths.CacheDir). The metadata cache
	// lives at <CacheDir>/_steamworkshop/meta/ and steamcmd's isolated home
	// at <CacheDir>/_steamworkshop/steamcmd-home/; the "_" prefix is
	// unreachable as a game slug, so it cannot collide with the
	// game-scoped mod cache that shares this root.
	CacheDir string
	// BaseURL overrides Valve's Web API base. Empty uses DefaultBaseURL.
	// A test MUST set this to an httptest server: reaching the real API
	// from a test is forbidden, and TestNoTestReachesTheProductionAPI
	// enforces it.
	BaseURL string
	// SteamRoots overrides Steam-installation discovery. Empty discovers
	// the roots the way `lmm game detect` already does (steam.FindSteamRoots,
	// which honours $STEAM_ROOT and $HOME). Tests set it, or sandbox $HOME.
	SteamRoots []string
	// Now is the clock the metadata cache's TTLs are judged against. nil
	// uses time.Now.
	Now func() time.Time
}

// Source is the Steam Workshop ModSource (#269).
//
// Tier 1 tracks items the Steam client already downloaded and checks them
// for updates, without ever touching their files. Tier 3 (download.go,
// steamcmd.go) adds downloads - the legacy file_url, or an anonymous
// steamcmd shell-out - for an item lmm should manage its own copy of.
// Search (Tier 2, bring-your-own key) lands in its own unit and is
// reported as unsupported until it does.
type Source struct {
	client     *client
	steamRoots []string
	// cacheDir is lmm's cache root, kept for steamcmd's isolated home (see
	// steamcmdHome). Empty means "no persistent home", which still works -
	// see Fetch - it just re-pays the tool's bootstrap every run.
	cacheDir string
}

var (
	_ source.ModSource               = (*Source)(nil)
	_ source.CapabilityReporter      = (*Source)(nil)
	_ source.TypeLabeler             = (*Source)(nil)
	_ source.WorkshopScanner         = (*Source)(nil)
	_ source.UpdateProgressReporter  = (*Source)(nil)
	_ source.RefreshingUpdateChecker = (*Source)(nil)
	_ source.BatchModDescriber       = (*Source)(nil)
)

// New constructs a Steam Workshop source.
func New(opts Options) *Source {
	return &Source{
		client:     newClient(opts),
		steamRoots: opts.SteamRoots,
		cacheDir:   opts.CacheDir,
	}
}

// ID returns the registry id ("steamworkshop").
func (s *Source) ID() string { return sourceID }

// Name returns the display name.
func (s *Source) Name() string { return "Steam Workshop" }

// TypeLabel reports this as a built-in source for `lmm source list`.
func (s *Source) TypeLabel() string { return "built-in" }

// Capabilities reports what this source can do today.
//
// Updates is the whole point of Tier 1. Search and Auth stay false until
// Tier 2 lands the bring-your-own-key QueryFiles client: claiming a
// capability whose call can only return ErrNotSupported would put a Steam
// Workshop row in `lmm search`'s source list that can never answer.
// Dependencies and Versions are false permanently - a Workshop item has
// neither concept.
func (s *Source) Capabilities() source.Capabilities {
	return source.Capabilities{Search: false, Dependencies: false, Updates: true, Auth: false, Versions: false}
}

// AuthURL: unsupported - Tier 1 is entirely keyless.
func (s *Source) AuthURL() string { return "" }

// ExchangeToken: unsupported - Steam has no OAuth flow lmm can use.
func (s *Source) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", sourceID, source.ErrNotSupported)
}

// GetDependencies: unsupported - a published file declares no dependencies.
func (s *Source) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", sourceID, source.ErrNotSupported)
}

// Search: unsupported until Tier 2 (#269 W2), which needs a user-supplied
// Steam Web API key - Valve's QueryFiles endpoint returns 403 without one.
func (s *Source) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, fmt.Errorf("source %q: searching: %w", sourceID, source.ErrNotSupported)
}

// ScanWorkshopItems implements source.WorkshopScanner: it reads the Steam
// client's own appworkshop manifest for appID out of every Steam library on
// this machine and reports the items it declares.
//
// Pure local reads. No network call, no write of any kind, and never a
// touch of the game's own files.
func (s *Source) ScanWorkshopItems(ctx context.Context, appID string) (source.WorkshopScan, error) {
	if err := ctx.Err(); err != nil {
		return source.WorkshopScan{}, err
	}
	libraries, warnings := s.libraries()
	scan, err := ScanLibraries(libraries, appID)
	if err != nil {
		return source.WorkshopScan{}, fmt.Errorf("source %q: scanning workshop items: %w", sourceID, err)
	}
	return source.WorkshopScan{
		Roots:    scan.Libraries,
		Items:    scan.Items,
		Warnings: append(warnings, scan.Warnings...),
	}, nil
}

// libraries resolves every Steam library path to scan, in Steam's own
// search order. A configured SteamRoots wins; otherwise discovery runs the
// same walk `lmm game detect` does. A library-folder read that fails warns
// and is skipped - one unreadable root must not hide the others.
func (s *Source) libraries() (paths []string, warnings []string) {
	roots := s.steamRoots
	if len(roots) == 0 {
		roots = steam.FindSteamRoots()
	}
	seen := make(map[string]bool)
	for _, root := range roots {
		libs, err := steam.GetLibraryPaths(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		for _, lib := range libs {
			if seen[lib] {
				continue
			}
			seen[lib] = true
			paths = append(paths, lib)
		}
	}
	return paths, warnings
}

// SourceURL returns the Steam Community page for a published file - the one
// click that resolves an item's raw creator steamid64 to a real author name
// (Tier 1 deliberately does not resolve it itself; that needs a key).
func SourceURL(fileID string) string {
	return "https://steamcommunity.com/sharedfiles/filedetails/?id=" + fileID
}
