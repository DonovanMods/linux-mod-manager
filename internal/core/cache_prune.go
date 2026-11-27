// Package core: cache_prune.go is the mod cache's prune (#678): `lmm cache
// prune`, `lmm cache status`, the web UI's Storage section, and the prune a
// flow that changes versions runs afterwards.
//
// THE RULE. Per mod, the cache keeps
//
//  1. every INSTALLED version - the current version of any installed row of
//     any profile of the game - always, never counted;
//  2. the newest N other versions, N = config.yaml's
//     cache.keep_previous_versions (default 3), newest by when the entry
//     was first committed (cache.CommittedName; an entry from before that
//     stamp, by its directory's modification time), ties by version order;
//  3. on top of those, every version a deployed symlink of a game using
//     the cache points into (in use, whatever the records say), and every
//     PROTECTED version: one an installed row rolls
//     back to (previous_version), one any profile's ref names (a locked or
//     pinned version, a collection pin and the pin a substitute stands in
//     for, a collection's skipped pin, a mod not installed yet), one any
//     snapshot of the game names (its rows, their rollback targets, its
//     profile document's refs).
//
// A mod NOTHING names - no installed row, no profile ref, no snapshot, in
// any game whose cache it is (an uninstall's --keep-cache) - is left whole,
// and reported (CacheUninstalledMod); only an explicit prune that asks
// (CachePruneOptions.IncludeUninstalled) removes it.
//
// Whose references count: every game whose cache directory IS this one -
// compared by identity (os.SameFile), so a cache_path reaching it through
// a symlink or another spelling shares it - plus every game the database,
// the profile directory or the snapshot directory names that games.yaml no
// longer lists, whose cache lmm cannot place any more: their references
// protect versions in every cache.
//
// A protected version already among the newest N uses one of the N slots;
// one older than them is kept on top. So after updates v1..v5 (installed
// v5, previous_version v4) the default keeps v5, v4, v3 and v2.
//
// What a version is: every entry of one source version - its plain tree
// and its variants (#614, #639, #644), and a legacy directory spelled
// <version>@<variant> - kept or pruned together. A reference names a
// version by its version string, so a reference to any variant keeps them
// all: when unsure, keep.
//
// What is never a candidate: the merged pak's synthetic entry (#197), and
// any directory in a mod's container that lmm's markers do not vouch for
// as an entry (cache.ScanRoot). Retained compile sources (#196) live INSIDE
// their version's entry and go with it; retained downloads
// (retained_download.go) live under the staging root, not the cache.
//
// When lmm cannot read everything that could reference a version - the
// database's rows, a profile, a snapshot - it prunes nothing of that
// game's cache and says why (CachePruneSkip): least frustration.
//
// Every prune that removes anything runs under beginOp, so no mutation is
// using an entry it removes: an explicit prune takes the slot itself, and
// a flow's own prune runs inside the flow's slot, after the flow's work.
package core

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// CachePruneEntry is one cache entry a prune removes (or removed): one
// tree of one version of one mod.
type CachePruneEntry struct {
	// GameID is the game whose cache holds the entry.
	GameID string `json:"game_id"`
	// SourceID and ModID name the mod.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// ModName is the mod's name, when an installed row records one.
	ModName string `json:"mod_name,omitempty"`
	// Version is the entry's source version.
	Version string `json:"version"`
	// Variant names the entry's tree when it is not the version's plain
	// one (a FOMOD install's choices, a file set, an imported archive).
	Variant string `json:"variant,omitempty"`
	// Bytes is the room the entry takes on disk.
	Bytes int64 `json:"bytes"`
	// Uninstalled marks an entry of a mod nothing references, which only
	// a prune that includes them removes (CachePruneOptions).
	Uninstalled bool `json:"uninstalled,omitzero"`

	// path is the entry's directory; group, the version it is kept or
	// pruned with (versionOf).
	path  string
	group string
}

// CachePruneSkip is a game's cache a prune left whole, and why: something
// that could reference a version could not be read.
type CachePruneSkip struct {
	GameID string `json:"game_id"`
	Reason string `json:"reason"`
}

// CacheUninstalledMod is a mod whose cached versions nothing references -
// no installed row, profile ref or snapshot in any game whose cache it is
// (#678): left whole unless a prune includes them.
type CacheUninstalledMod struct {
	GameID   string `json:"game_id"`
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Versions is how many versions are cached; Bytes, the room they take.
	Versions int   `json:"versions"`
	Bytes    int64 `json:"bytes"`
}

// CachePruneOptions are a prune's choices.
type CachePruneOptions struct {
	// IncludeUninstalled also removes every cached version of each mod
	// nothing references (`lmm cache prune --include-uninstalled`).
	IncludeUninstalled bool
}

// CachePrunePlan is what `lmm cache prune` would remove - its --dry-run
// document, and the web UI's prune preview.
type CachePrunePlan struct {
	// KeepPreviousVersions is the setting the plan applied: previous
	// versions kept per mod, or -1 for unlimited (nothing is pruned).
	KeepPreviousVersions int `json:"keep_previous_versions"`
	// Games are the games whose caches the plan covers.
	Games []string `json:"games"`
	// Entries are the entries the prune removes, by game and mod, each
	// mod's newest first.
	Entries []CachePruneEntry `json:"entries"`
	// Versions is how many versions Entries remove; Bytes, how much room.
	Versions int   `json:"versions"`
	Bytes    int64 `json:"bytes"`
	// Skipped are the games whose caches are left whole, and why.
	Skipped []CachePruneSkip `json:"skipped,omitempty"`
	// IncludeUninstalled echoes the option: Entries then also hold every
	// version of the mods Uninstalled lists.
	IncludeUninstalled bool `json:"include_uninstalled,omitzero"`
	// Uninstalled are the mods nothing references, included or not.
	Uninstalled []CacheUninstalledMod `json:"uninstalled,omitempty"`

	// games and opts are what the plan covers, for the apply's re-plan.
	games []*domain.Game
	opts  CachePruneOptions
}

// CachePruneResult is what a prune removed - `lmm cache prune`'s document.
type CachePruneResult struct {
	// Entries are the entries removed.
	Entries []CachePruneEntry `json:"entries"`
	// Versions is how many versions Entries were; Bytes, the room freed.
	Versions int   `json:"versions"`
	Bytes    int64 `json:"bytes"`
	// Skipped are the games whose caches were left whole, and why.
	Skipped []CachePruneSkip `json:"skipped,omitempty"`
	// Warnings are entries the prune could not remove.
	Warnings []string `json:"warnings,omitempty"`
}

// CachePruneSummary is the prune a flow that changes versions runs after
// its work (#678): on its plan, what it will free; on its result, what it
// freed.
type CachePruneSummary struct {
	// Versions is how many old versions; Bytes, how much room.
	Versions int   `json:"versions"`
	Bytes    int64 `json:"bytes"`
	// Entries are the entries, as an explicit prune lists them.
	Entries []CachePruneEntry `json:"entries"`
}

// Sentence is the summary as both frontends word it: "frees 1.2 GiB (2 old
// versions)".
func (c *CachePruneSummary) Sentence() string {
	return fmt.Sprintf("frees %s (%s)", formatBytes(uint64(max(c.Bytes, 0))), oldVersions(c.Versions))
}

// FreedSentence is the summary of a prune that ran: "freed 1.2 GiB (2 old
// versions)".
func (c *CachePruneSummary) FreedSentence() string {
	return fmt.Sprintf("freed %s (%s)", formatBytes(uint64(max(c.Bytes, 0))), oldVersions(c.Versions))
}

// oldVersions is "1 old version" or "n old versions".
func oldVersions(n int) string {
	if n == 1 {
		return "1 old version"
	}
	return fmt.Sprintf("%d old versions", n)
}

// CacheStatus is `lmm cache status`'s document: the cache's size per game
// and what a prune would reclaim.
type CacheStatus struct {
	// KeepPreviousVersions is the setting: previous versions kept per mod,
	// or -1 for unlimited.
	KeepPreviousVersions int `json:"keep_previous_versions"`
	// Games is one row per cache location, by game.
	Games []CacheGameStatus `json:"games"`
	// Bytes is every location's size; ReclaimableBytes and
	// ReclaimableVersions, what a prune of them all would free.
	Bytes               int64 `json:"bytes"`
	ReclaimableBytes    int64 `json:"reclaimable_bytes"`
	ReclaimableVersions int   `json:"reclaimable_versions"`
	// UninstalledBytes is the room mods nothing references take, which a
	// prune frees only when asked.
	UninstalledBytes int64 `json:"uninstalled_bytes,omitzero"`
}

// CacheGameStatus is one game's cache.
type CacheGameStatus struct {
	// GameID and GameName name the game.
	GameID   string `json:"game_id"`
	GameName string `json:"game_name"`
	// Path is the directory the game's mods are cached in.
	Path string `json:"path"`
	// SharedWith names the other games whose cache_path is the same
	// directory; their sizes are this row's.
	SharedWith []string `json:"shared_with,omitempty"`
	// Bytes is everything under Path.
	Bytes int64 `json:"bytes"`
	// Mods is how many mods have cached versions; Versions, how many
	// versions those are.
	Mods     int `json:"mods"`
	Versions int `json:"versions"`
	// ReclaimableBytes and ReclaimableVersions are what a prune would
	// free.
	ReclaimableBytes    int64 `json:"reclaimable_bytes"`
	ReclaimableVersions int   `json:"reclaimable_versions"`
	// Skipped says why a prune would leave this cache whole, when it
	// would.
	Skipped string `json:"skipped,omitempty"`
	// Uninstalled are the mods nothing references, which a prune leaves
	// whole unless asked; UninstalledBytes, the room they take.
	Uninstalled      []CacheUninstalledMod `json:"uninstalled,omitempty"`
	UninstalledBytes int64                 `json:"uninstalled_bytes,omitzero"`
}

// CacheKeepPreviousVersions is config.yaml's cache.keep_previous_versions
// as it stands NOW - read on every call, so `lmm cache keep` from a
// terminal reaches a running `lmm serve` - as previous versions kept per
// mod, or -1 for unlimited. A config.yaml that cannot be read, or is not a
// regular file, is an error, and a caller then prunes nothing.
func (s *Service) CacheKeepPreviousVersions() (int, error) {
	if s.configDir == "" {
		if s.config == nil {
			return config.DefaultCacheKeepPreviousVersions, nil
		}
		return s.config.CacheKeepPreviousVersions(), nil
	}
	path := filepath.Join(s.configDir, "config.yaml")
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return config.DefaultCacheKeepPreviousVersions, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading cache.keep_previous_versions: %w", err)
	}
	if !info.Mode().IsRegular() {
		// Never open a FIFO or a device: a read could block a flow.
		return 0, fmt.Errorf("reading cache.keep_previous_versions: %s is not a regular file", path)
	}
	cfg, err := config.Load(s.configDir)
	if err != nil {
		return 0, fmt.Errorf("reading cache.keep_previous_versions: %w", err)
	}
	return cfg.CacheKeepPreviousVersions(), nil
}

// SetCacheKeepPreviousVersions writes config.yaml's
// cache.keep_previous_versions, keeping every other setting: the web UI's
// setting and `lmm cache keep`. Any negative count is unlimited. Like
// SetAutoUpdate it is a config.yaml write, not a mutation of anything a
// flow touches, so it takes no mutation slot; lockConfig keeps it from
// losing a concurrent write.
func (s *Service) SetCacheKeepPreviousVersions(ctx context.Context, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockConfig(s.configDir)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load(s.configDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg.SetCacheKeepPreviousVersions(n)
	if err := cfg.Save(s.configDir); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

// pruneChange is one installed row a flow is about to change, for a plan's
// prediction of the prune that follows it: the row of the mod in the
// profile will be at Version, rolling back to Previous. An empty Version
// is a version the flow resolves itself.
type pruneChange struct {
	GameID, Profile, SourceID, ModID string
	Version, Previous                string
}

// cacheLocation is one directory of mod containers and every game whose
// cache it is.
type cacheLocation struct {
	path string
	// game is the game the location is reported under.
	game *domain.Game
	// sharing is every game whose cache this directory is, game included.
	sharing []*domain.Game
}

// cacheLocations is the locations of games' caches, one per directory,
// each with every game that shares it - any configured game, and any of
// games themselves - compared by identity (sameDir).
func (s *Service) cacheLocations(games []*domain.Game) []cacheLocation {
	candidates := map[string]*domain.Game{}
	for _, g := range s.ListGames() {
		candidates[g.ID] = g
	}
	for _, g := range games {
		candidates[g.ID] = g
	}
	all := slices.SortedFunc(maps.Values(candidates), func(a, b *domain.Game) int { return cmp.Compare(a.ID, b.ID) })
	roots := make(map[string]string, len(all))
	for _, g := range all {
		roots[g.ID] = s.cacheRootOf(g)
	}
	sorted := slices.Clone(games)
	slices.SortFunc(sorted, func(a, b *domain.Game) int { return cmp.Compare(a.ID, b.ID) })
	var out []cacheLocation
	for _, g := range sorted {
		path := s.cacheRootOf(g)
		if slices.ContainsFunc(out, func(l cacheLocation) bool { return sameDir(l.path, path) }) {
			continue
		}
		loc := cacheLocation{path: path, game: g, sharing: []*domain.Game{g}}
		for _, other := range all {
			if other.ID != g.ID && sameDir(roots[other.ID], path) {
				loc.sharing = append(loc.sharing, other)
			}
		}
		out = append(out, loc)
	}
	return out
}

// cacheRootOf is the directory game's mod containers live in.
func (s *Service) cacheRootOf(game *domain.Game) string {
	return filepath.Clean(s.GetGameCache(game).Root(game.ID))
}

// sameDir reports whether a and b are one directory: the same file when
// both exist (a symlink or another spelling of the path included), else
// the same path once symlinks and dots are resolved as far as they can be.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ia, ib)
	}
	return resolvedCacheDir(a) == resolvedCacheDir(b)
}

// resolvedCacheDir is p with symlinks resolved in its longest existing
// ancestor, and cleaned.
func resolvedCacheDir(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// orphanGameIDs is every game ID lmm still holds records for that
// games.yaml no longer lists - an installed row, a profile directory, a
// snapshot directory - whose cache directory lmm therefore cannot place:
// their references protect versions in every cache.
func (s *Service) orphanGameIDs(ctx context.Context, known []*domain.Game) ([]string, error) {
	listed := map[string]bool{}
	for _, g := range s.ListGames() {
		listed[g.ID] = true
	}
	for _, g := range known {
		listed[g.ID] = true
	}
	found := map[string]bool{}
	ids, err := s.db.InstalledGameIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		found[id] = true
	}
	for _, dir := range []string{filepath.Join(s.configDir, "games"), filepath.Join(s.dataDir, "snapshots")} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "_") {
				found[e.Name()] = true
			}
		}
	}
	var out []string
	for id := range found {
		if !listed[id] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// pruneRefs is everything that keeps a version, for one cache location,
// keyed by refKey.
type pruneRefs struct {
	installed map[string]bool
	protected map[string]bool
	// names and ids are each mod container's display name and identity,
	// as an installed row or ref records them; a container ids lacks is
	// one nothing references.
	names map[string]string
	ids   map[string][2]string
}

// referenced reports whether anything names a version of the mod in
// container modDir.
func (r *pruneRefs) referenced(modDir string) bool {
	_, ok := r.ids[modDir]
	return ok
}

// refKey keys a version of the mod in container modDir.
func refKey(modDir, version string) string { return modDir + "\x00" + version }

// snapshotRefs is the part of a snapshot document a prune reads.
type snapshotRefs struct {
	ProfileDocument *domain.ExportedProfile `json:"profile_document"`
	Installed       []domain.InstalledMod   `json:"installed"`
}

// readPruneRefs reads everything that keeps a version in loc - every
// sharing game's, and every orphan game's, installed rows, profiles and
// snapshots - with changes applied over the rows and refs they replace.
// Any read that fails is an error: the location is then left whole.
func (s *Service) readPruneRefs(ctx context.Context, loc cacheLocation, orphans []string, changes []pruneChange) (*pruneRefs, error) {
	refs := &pruneRefs{installed: map[string]bool{}, protected: map[string]bool{}, names: map[string]string{}, ids: map[string][2]string{}}
	changed := map[string]pruneChange{}
	for _, c := range changes {
		changed[c.GameID+"\x00"+c.Profile+"\x00"+domain.ModKey(c.SourceID, c.ModID)] = c
	}
	isChanged := func(gameID, profile, sourceID, modID string) bool {
		_, ok := changed[gameID+"\x00"+profile+"\x00"+domain.ModKey(sourceID, modID)]
		return ok
	}
	note := func(sourceID, modID, name string) string {
		dir := cache.ModDirName(sourceID, modID)
		if _, ok := refs.ids[dir]; !ok {
			refs.ids[dir] = [2]string{sourceID, modID}
		}
		if name != "" && refs.names[dir] == "" {
			refs.names[dir] = name
		}
		return dir
	}
	protect := func(dir, version string) {
		if version != "" {
			refs.protected[refKey(dir, version)] = true
		}
	}
	install := func(dir, version string) {
		if version != "" {
			refs.installed[refKey(dir, version)] = true
		}
	}
	type gameRefs struct {
		id     string
		orphan bool
	}
	var games []gameRefs
	gameIDs := map[string]bool{}
	for _, g := range loc.sharing {
		games = append(games, gameRefs{id: g.ID})
		gameIDs[g.ID] = true
	}
	for _, id := range orphans {
		if !gameIDs[id] {
			games = append(games, gameRefs{id: id, orphan: true})
		}
	}
	for _, g := range games {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := s.db.GameVersionRows(ctx, g.id)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			dir := note(row.SourceID, row.ModID, row.Name)
			if isChanged(g.id, row.Profile, row.SourceID, row.ModID) {
				continue
			}
			install(dir, row.Version)
			protect(dir, row.PreviousVersion)
		}
		names, err := config.ListProfiles(s.configDir, g.id)
		if err != nil {
			if g.orphan && errors.Is(err, domain.ErrInvalidGameID) {
				continue // a directory name that is no game ID names no game
			}
			return nil, fmt.Errorf("listing the profiles of %s: %w", g.id, err)
		}
		for _, name := range names {
			profile, err := config.LoadProfile(s.configDir, g.id, name)
			if err != nil {
				return nil, fmt.Errorf("reading profile %s of %s: %w", name, g.id, err)
			}
			for _, ref := range profile.Mods {
				dir := note(ref.SourceID, ref.ModID, "")
				if !isChanged(g.id, name, ref.SourceID, ref.ModID) {
					protect(dir, ref.Version)
				}
				if ref.SubstitutedFor != nil {
					protect(dir, ref.SubstitutedFor.Version)
				}
			}
			if b := profile.CollectionBaseline; b != nil {
				for _, skip := range b.Skipped {
					protect(note(skip.SourceID, skip.ModID, ""), skip.Pin.Version)
				}
			}
		}
		if err := s.readSnapshotRefs(ctx, g.id, func(sourceID, modID, version string) {
			protect(note(sourceID, modID, ""), version)
		}); err != nil {
			return nil, err
		}
	}
	for _, c := range changes {
		if !gameIDs[c.GameID] {
			continue
		}
		dir := note(c.SourceID, c.ModID, "")
		install(dir, c.Version)
		protect(dir, c.Previous)
	}
	return refs, nil
}

// readSnapshotRefs calls ref for every version a snapshot of gameID
// names. A snapshot that cannot be read is an error.
func (s *Service) readSnapshotRefs(ctx context.Context, gameID string, ref func(sourceID, modID, version string)) error {
	dir := snapshotsDirFor(s.dataDir, gameID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the snapshots of %s: %w", gameID, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), snapshotFileExt) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading snapshot %s: %w", path, err)
		}
		var doc snapshotRefs
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("reading snapshot %s: %w", path, err)
		}
		for _, row := range doc.Installed {
			ref(row.SourceID, row.ID, row.Version)
			ref(row.SourceID, row.ID, row.PreviousVersion)
		}
		if doc.ProfileDocument != nil {
			for _, m := range doc.ProfileDocument.Mods {
				ref(m.SourceID, m.ModID, m.Version)
			}
		}
	}
	return nil
}

// cachedVersion is one version of a mod found in its container: every
// entry of it, and when the newest of them was cached.
type cachedVersion struct {
	version string
	entries []cache.ScannedEntry
	at      time.Time
}

// versionOf is the version e is kept or pruned with: its own, or - for a
// legacy directory spelled <version>@<variant> - the version before the
// separator, so a variant from before identity markers neither takes a
// slot of its own nor splits its version.
func versionOf(e cache.ScannedEntry) string {
	if i := strings.LastIndex(e.Version, cache.VariantSeparator); e.Legacy && i > 0 {
		return e.Version[:i]
	}
	return e.Version
}

// keeps reports whether set names v, or any of its entries by its own
// version.
func keeps(set map[string]bool, modDir string, v cachedVersion) bool {
	if set[refKey(modDir, v.version)] {
		return true
	}
	for _, e := range v.entries {
		if set[refKey(modDir, e.Version)] {
			return true
		}
	}
	return false
}

// pruneScan is what one scan of a location is for.
type pruneScan struct {
	keep int
	// only names the mod containers to scan; nil scans every one.
	only    []string
	changes []pruneChange
	// includeUninstalled makes every version of a mod nothing references
	// a candidate.
	includeUninstalled bool
}

// locationScan is one location's cache as a prune sees it.
type locationScan struct {
	candidates  []CachePruneEntry
	versions    int // candidate versions
	mods        int // containers holding entries
	all         int // versions cached
	uninstalled []CacheUninstalledMod
	skipped     string
}

// scanLocation finds the entries a prune of loc removes. A location whose
// references cannot be read is returned skipped, with no candidates. With
// keep < 0 (unlimited) it only counts.
func (s *Service) scanLocation(ctx context.Context, loc cacheLocation, scan pruneScan) (locationScan, error) {
	var out locationScan
	var mods []cache.ScannedMod
	var err error
	if scan.only != nil {
		mods, err = cache.ScanContainers(loc.path, scan.only)
	} else {
		mods, err = cache.ScanRoot(loc.path)
	}
	if err != nil {
		out.skipped = err.Error()
		return out, nil
	}
	var refs *pruneRefs
	if scan.keep >= 0 {
		orphans, err := s.orphanGameIDs(ctx, loc.sharing)
		if err == nil {
			refs, err = s.readPruneRefs(ctx, loc, orphans, scan.changes)
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return out, ctxErr
			}
			out.skipped = err.Error()
			refs = nil
		}
	}
	// #678 re-review: what is deployed is in use, whatever the records
	// say - a failed update can leave a version deployed whose row was
	// never saved.
	var deployed map[string][]string
	if refs != nil {
		if deployed, err = s.deployedCacheTargets(ctx, loc, scan.only); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return out, ctxErr
			}
			out.skipped = "reading what is deployed: " + err.Error()
			refs = nil
		}
		// A plan's prediction: the flow redeploys each mod it changes, so
		// what those mods have deployed now says nothing about afterwards
		// (the prune that runs after the flow reads the game again).
		for _, c := range scan.changes {
			delete(deployed, cache.ModDirName(c.SourceID, c.ModID))
		}
	}
	for _, m := range mods {
		if strings.HasPrefix(m.Dir, domain.SourceMerged+"-") || len(m.Entries) == 0 {
			continue
		}
		versions := groupVersions(m.Entries)
		out.mods++
		out.all += len(versions)
		if refs == nil {
			continue
		}
		if !refs.referenced(m.Dir) && len(deployed[m.Dir]) == 0 {
			s.scanUninstalled(loc, m, versions, refs, scan, &out)
			continue
		}
		previous := 0
		for _, v := range versions {
			if keeps(refs.installed, m.Dir, v) || deployedFrom(deployed[m.Dir], v) {
				continue
			}
			previous++
			if previous <= scan.keep || keeps(refs.protected, m.Dir, v) {
				continue
			}
			entries, ok := s.sizeVersion(loc, m.Dir, v, refs)
			if !ok {
				continue
			}
			out.candidates = append(out.candidates, entries...)
			out.versions++
		}
	}
	return out, nil
}

// deployedCacheTargets is where every deployed symlink of loc's games
// points inside loc, by mod container: each target resolved (symlinks and
// dots), for the containers only names (every one when nil). A deployed
// file that is not a symlink - a copy or a hard link - does not need the
// cache and is not counted. Any path it cannot read is an error, and the
// caller then leaves the location whole.
func (s *Service) deployedCacheTargets(ctx context.Context, loc cacheLocation, only []string) (map[string][]string, error) {
	root := resolvedCacheDir(loc.path)
	var wanted map[string]bool
	if only != nil {
		wanted = make(map[string]bool, len(only))
		for _, dir := range only {
			wanted[dir] = true
		}
	}
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, g := range loc.sharing {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		records, err := s.db.DeployedPathRecords(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for p, recs := range records {
			for _, r := range recs {
				container := cache.ModDirName(r.SourceID, r.ModID)
				if wanted != nil && !wanted[container] {
					continue
				}
				target := deployTarget(g, p)
				if r.ModPath != "" && p.Root == domain.RootModPath {
					target = filepath.Join(r.ModPath, filepath.FromSlash(p.Rel))
				}
				if target == "" || seen[target] {
					continue
				}
				seen[target] = true
				resolved, ok, err := symlinkTarget(target)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
				rel, err := filepath.Rel(root, resolved)
				if err != nil || rel == "." || !filepath.IsLocal(rel) {
					continue
				}
				dir := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
				out[dir] = append(out[dir], resolved)
			}
		}
	}
	return out, nil
}

// symlinkTarget is the resolved target of the deployed file at path when it
// is a symlink; ok is false when it is not one, or is not there.
func symlinkTarget(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading deployed file %s: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "", false, nil
	}
	link, err := os.Readlink(path)
	if err != nil {
		return "", false, fmt.Errorf("reading deployed file %s: %w", path, err)
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return resolvedCacheDir(link), true, nil
}

// deployedFrom reports whether a deployed symlink's target (targets, of
// v's container) lies inside one of v's entries.
func deployedFrom(targets []string, v cachedVersion) bool {
	if len(targets) == 0 {
		return false
	}
	for _, e := range v.entries {
		dir := resolvedCacheDir(e.Path) + string(filepath.Separator)
		for _, t := range targets {
			if strings.HasPrefix(t, dir) {
				return true
			}
		}
	}
	return false
}

// scanUninstalled records a mod nothing references: listed with its size,
// and every version a candidate only when the scan includes them.
func (s *Service) scanUninstalled(loc cacheLocation, m cache.ScannedMod, versions []cachedVersion, refs *pruneRefs, scan pruneScan, out *locationScan) {
	sourceID, modID := s.splitModDir(m.Dir, refs)
	mod := CacheUninstalledMod{GameID: loc.game.ID, SourceID: sourceID, ModID: modID}
	for _, v := range versions {
		entries, ok := s.sizeVersion(loc, m.Dir, v, refs)
		if !ok {
			continue
		}
		mod.Versions++
		for i := range entries {
			mod.Bytes += entries[i].Bytes
			entries[i].Uninstalled = true
		}
		if scan.includeUninstalled {
			out.candidates = append(out.candidates, entries...)
			out.versions++
		}
	}
	if mod.Versions > 0 {
		out.uninstalled = append(out.uninstalled, mod)
	}
}

// groupVersions groups a container's entries by version (versionOf),
// newest first: by when the version's newest entry was cached
// (ScannedEntry.CachedAt), then by version order, then by name.
func groupVersions(entries []cache.ScannedEntry) []cachedVersion {
	byVersion := map[string]*cachedVersion{}
	var order []*cachedVersion
	for _, e := range entries {
		key := versionOf(e)
		v := byVersion[key]
		if v == nil {
			v = &cachedVersion{version: key}
			byVersion[key] = v
			order = append(order, v)
		}
		v.entries = append(v.entries, e)
		if at := e.CachedAt(); at.After(v.at) {
			v.at = at
		}
	}
	out := make([]cachedVersion, len(order))
	for i, v := range order {
		sort.Slice(v.entries, func(a, b int) bool { return v.entries[a].Path < v.entries[b].Path })
		out[i] = *v
	}
	slices.SortStableFunc(out, func(a, b cachedVersion) int {
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		if c := domain.CompareVersions(b.version, a.version); c != 0 {
			return c
		}
		return cmp.Compare(b.version, a.version)
	})
	return out
}

// sizeVersion is v's entries as prune candidates. ok is false - keep the
// version - when an entry cannot be sized, or holds another entry nested
// inside it.
func (s *Service) sizeVersion(loc cacheLocation, modDir string, v cachedVersion, refs *pruneRefs) ([]CachePruneEntry, bool) {
	sourceID, modID := s.splitModDir(modDir, refs)
	out := make([]CachePruneEntry, 0, len(v.entries))
	for _, e := range v.entries {
		bytes, nested, err := cache.DiskUsage(e.Path)
		if err != nil || nested {
			return nil, false
		}
		out = append(out, CachePruneEntry{
			GameID: loc.game.ID, SourceID: sourceID, ModID: modID, ModName: refs.names[modDir],
			Version: e.Version, Variant: e.Variant, Bytes: bytes, path: e.Path, group: v.version,
		})
	}
	return out, true
}

// splitModDir is the source and mod a container is for: as an installed
// row or ref names it, else split after the longest registered source ID
// it starts with, else at its first "-".
func (s *Service) splitModDir(modDir string, refs *pruneRefs) (string, string) {
	if id, ok := refs.ids[modDir]; ok {
		return id[0], id[1]
	}
	best := ""
	for _, src := range s.registry.List() {
		if id := src.ID(); strings.HasPrefix(modDir, id+"-") && len(id) > len(best) {
			best = id
		}
	}
	if best != "" {
		return best, strings.TrimPrefix(modDir, best+"-")
	}
	if source, mod, ok := strings.Cut(modDir, "-"); ok {
		return source, mod
	}
	return "", modDir
}

// pruneTargets is the games a prune or status covers: games, or every
// configured game when games is empty.
func (s *Service) pruneTargets(games []*domain.Game) []*domain.Game {
	if len(games) > 0 {
		return games
	}
	return s.ListGames()
}

// PlanCachePrune computes what a prune of games' caches removes - every
// configured game's when games is empty. It is the --dry-run: nothing is
// removed. A config.yaml that cannot be read refuses the plan.
func (s *Service) PlanCachePrune(ctx context.Context, games []*domain.Game, opts CachePruneOptions) (*CachePrunePlan, error) {
	keep, err := s.CacheKeepPreviousVersions()
	if err != nil {
		return nil, err
	}
	targets := s.pruneTargets(games)
	plan := &CachePrunePlan{
		KeepPreviousVersions: keep, Games: []string{}, Entries: []CachePruneEntry{},
		IncludeUninstalled: opts.IncludeUninstalled, games: targets, opts: opts,
	}
	for _, g := range targets {
		plan.Games = append(plan.Games, g.ID)
	}
	slices.Sort(plan.Games)
	if keep < 0 {
		return plan, nil
	}
	for _, loc := range s.cacheLocations(targets) {
		scan, err := s.scanLocation(ctx, loc, pruneScan{keep: keep, includeUninstalled: opts.IncludeUninstalled})
		if err != nil {
			return nil, err
		}
		if scan.skipped != "" {
			plan.Skipped = append(plan.Skipped, CachePruneSkip{GameID: loc.game.ID, Reason: scan.skipped})
			continue
		}
		plan.Entries = append(plan.Entries, scan.candidates...)
		plan.Uninstalled = append(plan.Uninstalled, scan.uninstalled...)
		plan.Versions += scan.versions
		for _, e := range scan.candidates {
			plan.Bytes += e.Bytes
		}
	}
	return plan, nil
}

// ApplyCachePrune removes what plan lists, under the mutation slot. The
// plan is computed again first: when what it would remove has changed
// since - a profile now references a version, an entry is gone - the
// apply refuses with ErrStalePlan and removes nothing. An entry that
// cannot be removed is a warning, not a failure.
func (s *Service) ApplyCachePrune(ctx context.Context, plan *CachePrunePlan, sink EventSink) (*CachePruneResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if plan == nil {
		return nil, errors.New("cache prune: no plan")
	}
	current, err := s.PlanCachePrune(ctx, plan.games, plan.opts)
	if err != nil {
		return nil, err
	}
	if !samePruneEntries(plan.Entries, current.Entries) {
		return nil, fmt.Errorf("%w: what the cache prune would remove has changed since it was planned", ErrStalePlan)
	}
	// What an interrupted removal left is swept here, under the slot -
	// never by a read.
	for _, loc := range s.cacheLocations(plan.games) {
		cache.SweepPruneLeftovers(loc.path, nil)
	}
	removed, warnings := s.removePruneEntries(ctx, current.Entries, Scope{Op: OpCachePrune}, sink)
	result := summarize(removed)
	return &CachePruneResult{
		Entries: result.Entries, Versions: result.Versions, Bytes: result.Bytes,
		Skipped: current.Skipped, Warnings: warnings,
	}, nil
}

// samePruneEntries reports whether a and b name the same entries.
func samePruneEntries(a, b []CachePruneEntry) bool {
	paths := func(es []CachePruneEntry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.path
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(paths(a), paths(b))
}

// removePruneEntries removes entries, reporting each to sink, and returns
// the ones removed and a warning for each that could not be.
func (s *Service) removePruneEntries(ctx context.Context, entries []CachePruneEntry, scope Scope, sink EventSink) ([]CachePruneEntry, []string) {
	var removed []CachePruneEntry
	var warnings []string
	for i, e := range entries {
		if ctx.Err() != nil {
			warnings = append(warnings, fmt.Sprintf("cache prune stopped: %v", ctx.Err()))
			break
		}
		if err := cache.RemoveEntry(e.path); err != nil {
			msg := fmt.Sprintf("could not prune version %s of %s from the cache: %v", e.Version, pruneModLabel(e), err)
			warnings = append(warnings, msg)
			if sink != nil {
				sink(WarningEvent{Scope: scope, Phase: CachePruneWarning, Message: msg})
			}
			continue
		}
		removed = append(removed, e)
		if sink != nil {
			sc := scope
			sc.Index, sc.Total, sc.ModName = i+1, len(entries), pruneModLabel(e)
			sink(ModEvent{Scope: sc, Phase: CachePruneRemoved, Version: e.Version})
		}
	}
	return removed, warnings
}

// pruneModLabel names an entry's mod for a message.
func pruneModLabel(e CachePruneEntry) string {
	if e.ModName != "" {
		return e.ModName
	}
	return domain.ModKey(e.SourceID, e.ModID)
}

// summarize totals entries as a summary.
func summarize(entries []CachePruneEntry) *CachePruneSummary {
	out := &CachePruneSummary{Entries: []CachePruneEntry{}}
	versions := map[string]bool{}
	for _, e := range entries {
		out.Entries = append(out.Entries, e)
		out.Bytes += e.Bytes
		group := e.group
		if group == "" {
			group = e.Version
		}
		versions[e.GameID+"\x00"+domain.ModKey(e.SourceID, e.ModID)+"\x00"+group] = true
	}
	out.Versions = len(versions)
	return out
}

// CacheStatus reports games' caches - every configured game's when games
// is empty: each location's size, what a prune would reclaim, and the
// mods nothing references. It reads every cached file's size, so it costs
// a walk of the cache.
func (s *Service) CacheStatus(ctx context.Context, games []*domain.Game) (*CacheStatus, error) {
	keep, err := s.CacheKeepPreviousVersions()
	if err != nil {
		return nil, err
	}
	status := &CacheStatus{KeepPreviousVersions: keep, Games: []CacheGameStatus{}}
	for _, loc := range s.cacheLocations(s.pruneTargets(games)) {
		row := CacheGameStatus{GameID: loc.game.ID, GameName: loc.game.Name, Path: loc.path}
		for _, other := range loc.sharing[1:] {
			row.SharedWith = append(row.SharedWith, other.ID)
		}
		slices.Sort(row.SharedWith)
		if bytes, _, err := cache.DiskUsage(loc.path); err == nil {
			row.Bytes = bytes
		} else if !errors.Is(err, fs.ErrNotExist) {
			row.Skipped = err.Error()
		}
		// Unlimited still lists the mods nothing references: the keep
		// count does not decide those.
		scanKeep := max(keep, 0)
		scan, err := s.scanLocation(ctx, loc, pruneScan{keep: scanKeep})
		if err != nil {
			return nil, err
		}
		row.Mods, row.Versions = scan.mods, scan.all
		if scan.skipped != "" && row.Skipped == "" {
			row.Skipped = scan.skipped
		}
		row.Uninstalled = scan.uninstalled
		for _, m := range scan.uninstalled {
			row.UninstalledBytes += m.Bytes
		}
		if keep >= 0 {
			row.ReclaimableVersions = scan.versions
			for _, e := range scan.candidates {
				row.ReclaimableBytes += e.Bytes
			}
		}
		status.Games = append(status.Games, row)
		status.Bytes += row.Bytes
		status.ReclaimableBytes += row.ReclaimableBytes
		status.ReclaimableVersions += row.ReclaimableVersions
		status.UninstalledBytes += row.UninstalledBytes
	}
	return status, nil
}

// modDirsOf is the containers of mods, sorted, as a scan's filter.
func modDirsOf(mods []domain.ModReference) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, cache.ModDirName(m.SourceID, m.ModID))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// predictCachePrune is what the prune that follows a flow's changes would
// free, for the flow's plan: nil when it frees nothing, when pruning is
// off, or when the prediction cannot be made (the flow's own prune then
// decides for itself). Called outside any mutation slot; it only reads.
func (s *Service) predictCachePrune(ctx context.Context, game *domain.Game, changes []pruneChange) *CachePruneSummary {
	if len(changes) == 0 {
		return nil
	}
	keep, err := s.CacheKeepPreviousVersions()
	if err != nil || keep < 0 {
		return nil
	}
	mods := make([]domain.ModReference, len(changes))
	for i, c := range changes {
		mods[i] = domain.ModReference{SourceID: c.SourceID, ModID: c.ModID}
	}
	var entries []CachePruneEntry
	for _, loc := range s.cacheLocations([]*domain.Game{game}) {
		scan, err := s.scanLocation(ctx, loc, pruneScan{keep: keep, only: modDirsOf(mods), changes: changes})
		if err != nil || scan.skipped != "" {
			return nil
		}
		entries = append(entries, scan.candidates...)
	}
	if len(entries) == 0 {
		return nil
	}
	return summarize(entries)
}

// pruneAfterFlow prunes the old versions of mods in game's cache, after a
// flow changed their versions (#678). It runs inside the flow's mutation
// slot, and only after the flow succeeded: a flow that failed or was
// cancelled never calls it. It never fails the flow: what it could not do
// comes back as warnings, and a cache it could not judge - a config.yaml
// it cannot read included - is left whole. A mod nothing references is
// never pruned here. nil when it removed nothing.
//
// It is bounded by the mods the flow changed - only their containers are
// read - so it does not stop for the flow's cancellation: a flow that
// finished finishes its housekeeping too.
func (s *Service) pruneAfterFlow(ctx context.Context, game *domain.Game, mods []domain.ModReference, scope Scope, sink EventSink) (*CachePruneSummary, []string) {
	if len(mods) == 0 {
		return nil, nil
	}
	ctx = context.WithoutCancel(ctx)
	keep, err := s.CacheKeepPreviousVersions()
	if err != nil {
		return nil, []string{warnPrune(scope, sink, fmt.Sprintf("old versions were not pruned from the cache: %v", err))}
	}
	if keep < 0 {
		return nil, nil
	}
	dirs := modDirsOf(mods)
	var removed []CachePruneEntry
	var warnings []string
	for _, loc := range s.cacheLocations([]*domain.Game{game}) {
		cache.SweepPruneLeftovers(loc.path, dirs)
		scan, err := s.scanLocation(ctx, loc, pruneScan{keep: keep, only: dirs})
		if err != nil {
			warnings = append(warnings, warnPrune(scope, sink, fmt.Sprintf("could not prune old versions from the cache: %v", err)))
			continue
		}
		if scan.skipped != "" {
			warnings = append(warnings, warnPrune(scope, sink, fmt.Sprintf("old versions were not pruned from the cache: %s", scan.skipped)))
			continue
		}
		done, warns := s.removePruneEntries(ctx, scan.candidates, scope, sink)
		removed = append(removed, done...)
		warnings = append(warnings, warns...)
	}
	if len(removed) == 0 {
		return nil, warnings
	}
	summary := summarize(removed)
	if sink != nil {
		sink(StepEvent{Scope: scope, Phase: CachePruneDone, Detail: summary.FreedSentence()})
	}
	return summary, warnings
}

// warnPrune reports a flow prune's warning to sink and returns it.
func warnPrune(scope Scope, sink EventSink, msg string) string {
	if sink != nil {
		sink(WarningEvent{Scope: scope, Phase: CachePruneWarning, Message: msg})
	}
	return msg
}

// convergeChanges is a converge plan's installs as prune changes, for its
// prediction: each one that replaces a row rolls back to the row's
// version, and each one for a mod the profile has no row of (snapshot,
// the plan's installed set) is new. Any other - a row the flow replaces
// without retiring it - is left out, so its row keeps its version: the
// prediction errs toward freeing less.
func convergeChanges(gameID, profile string, installs []ProfileApplyInstall, snapshot installedSnapshot) []pruneChange {
	var out []pruneChange
	for _, in := range installs {
		key := domain.ModKey(in.Ref.SourceID, in.Ref.ModID)
		change := pruneChange{GameID: gameID, Profile: profile, SourceID: in.Ref.SourceID, ModID: in.Ref.ModID, Version: in.Version}
		switch {
		case in.Replaces != nil:
			change.Previous = in.Replaces.Version
		case snapshot != nil:
			if _, had := snapshot[key]; had {
				continue
			}
		default:
			continue
		}
		out = append(out, change)
	}
	return out
}

// changedOutcomeMods is the mods an apply installed or replaced.
func changedOutcomeMods(outcomes []ProfileApplyOutcome) []domain.ModReference {
	var out []domain.ModReference
	for _, o := range outcomes {
		if o.Outcome == ProfileApplyInstalled || o.Outcome == ProfileApplyReplaced {
			out = append(out, domain.ModReference{SourceID: o.SourceID, ModID: o.ModID})
		}
	}
	return out
}
