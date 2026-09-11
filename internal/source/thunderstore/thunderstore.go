// Package thunderstore is the built-in Thunderstore source (#360).
//
// Thunderstore publishes ONE unpaginated document per community and no
// per-query search endpoint at all, so "search Thunderstore" here means
// "search a local copy": the source keeps a split index under
// <CacheDir>/_thunderstore/<community>/, built by a streaming decode that
// never holds the whole document in memory, refreshed on a TTL with a
// conditional GET, and searched with a full scan. index.go owns the fetch
// and the on-disk layout, search.go the resident copy and its ranking.
//
// This file is the ModSource itself: identity, capabilities, construction.
// No credential exists for any of it - Thunderstore needs no key for
// search, metadata or downloads, which makes this the first built-in that
// reports Auth: false.
package thunderstore

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// sourceID is the registry id a game maps to in games.yaml's sources block;
// the per-source game id under it IS the Thunderstore community slug
// (`sources: {thunderstore: lethal-company}`).
const sourceID = "thunderstore"

// DefaultBaseURL is Thunderstore's public host. Tests MUST override it with
// an httptest server (Options.BaseURL): reaching the real site from a test
// is forbidden, and TestNoTestReachesTheProductionAPI enforces that no test
// in this package names the host at all.
const DefaultBaseURL = "https://thunderstore.io"

// Options configures New. Every field has a working zero value except
// CacheDir, without which there is nowhere to keep an index and every
// search fails with ErrIndexUnavailable - correct, and loud, rather than
// silently re-downloading 34 MB per query.
type Options struct {
	// HTTPClient is the transport for the community listing. nil builds one
	// with a generous timeout: the largest community on the site is 34 MB
	// on the wire.
	HTTPClient *http.Client
	// CacheDir is lmm's cache root (app.Paths.CacheDir). The index lives at
	// <CacheDir>/_thunderstore/<community>/; the "_" prefix is unreachable
	// as a game slug (core.DeriveGameID never emits one), so this tree
	// cannot collide with the game-scoped mod cache that shares the root.
	CacheDir string
	// BaseURL overrides Thunderstore's host. Empty uses DefaultBaseURL.
	BaseURL string
	// Now is the clock the index TTL is judged against. nil uses time.Now.
	Now func() time.Time
	// MaxIndexBytes caps ONE community document, measured on the stream
	// after net/http has decompressed it. 0 uses maxIndexBytes, which is
	// the only value production ever wants; a test sets it small so that
	// proving the ceiling works does not mean serving half a gigabyte.
	MaxIndexBytes int64
}

// Source is the Thunderstore ModSource (#360).
//
// Safe for concurrent use: the on-disk index is written whole-file with a
// rename, and the resident search copy is guarded by a mutex. `lmm serve`
// holds one resident index per community it has been asked about.
type Source struct {
	client *client
	store  *store
	now    func() time.Time

	// mu guards resident and locks below. Held only to look one up, never
	// across a fetch or a decode.
	mu sync.Mutex
	// resident is the loaded search index per community. Load is lazy - a
	// process that never searches never pays the 42 MB worst case - and a
	// refresh that moves the watermark invalidates it.
	resident map[string]*residentIndex
	// locks serializes index builds PER COMMUNITY: a cold build for one
	// community must not block a search of another that is already cached,
	// and two concurrent searches of the same community must not both
	// download 34 MB.
	locks map[string]*sync.Mutex
}

var (
	_ source.ModSource          = (*Source)(nil)
	_ source.CapabilityReporter = (*Source)(nil)
	_ source.TypeLabeler        = (*Source)(nil)
	_ source.LocalIndexSource   = (*Source)(nil)
)

// New constructs a Thunderstore source.
func New(opts Options) *Source {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Source{
		client:   newClient(opts, now),
		store:    newStore(opts.CacheDir),
		now:      now,
		resident: make(map[string]*residentIndex),
		locks:    make(map[string]*sync.Mutex),
	}
}

// ID returns the registry id ("thunderstore").
func (s *Source) ID() string { return sourceID }

// Name returns the display name.
func (s *Source) Name() string { return "Thunderstore" }

// TypeLabel reports this as a built-in source for `lmm source list`.
func (s *Source) TypeLabel() string { return "built-in" }

// Capabilities reports what this source can do today.
//
// Auth is false permanently and is the headline: Thunderstore needs no key
// at all - not for search, not for metadata, not for downloads - so this
// source implements no EnvKeyProvider and `lmm auth status` has nothing to
// report for it.
//
// Search is true from T1 (#408): the local index answers it. Versions is
// true from T2 (#409): a package version IS a file here, so GetModFiles
// returns one file per version and core.ResolveVersionFiles resolves an
// exact version against it. Dependencies and Updates follow in the same
// unit, each in the commit that implements it - declaring a capability
// before the method exists is a false claim to every frontend that branches
// on one.
func (s *Source) Capabilities() source.Capabilities {
	return source.Capabilities{Search: true, Dependencies: false, Updates: false, Auth: false, Versions: true}
}

// AuthURL: unsupported - there is no credential to obtain.
func (s *Source) AuthURL() string { return "" }

// ExchangeToken: unsupported - there is no credential to exchange.
func (s *Source) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, fmt.Errorf("source %q: authentication: %w", sourceID, source.ErrNotSupported)
}

// GetDependencies: T2 (#360) - parses the installed version's dependencies
// and routes the BepInEx loader entry out.
func (s *Source) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", sourceID, source.ErrNotSupported)
}

// CheckUpdates: T2 (#360) - an entirely local comparison against the index.
func (s *Source) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, fmt.Errorf("source %q: update check: %w", sourceID, source.ErrNotSupported)
}
