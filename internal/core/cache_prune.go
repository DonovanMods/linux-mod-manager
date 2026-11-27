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
//     was last written;
//  3. on top of those, every PROTECTED version: one an installed row rolls
//     back to (previous_version), one any profile's ref names (a locked or
//     pinned version, a collection pin, a mod not installed yet), one any
//     snapshot of the game names (its rows, their rollback targets, its
//     profile document's refs).
//
// A protected version already among the newest N uses one of the N slots;
// one older than them is kept on top. So after updates v1..v5 (installed
// v5, previous_version v4) the default keeps v5, v4, v3 and v2.
//
// What a version is: every entry of one source version - its plain tree
// and its variants (#614, #639, #644) - kept or pruned together. A
// reference names a version by its version string, so a reference to any
// variant keeps them all: when unsure, keep.
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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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

	// path is the entry's directory.
	path string
}

// CachePruneSkip is a game's cache a prune left whole, and why: something
// that could reference a version could not be read.
type CachePruneSkip struct {
	GameID string `json:"game_id"`
	Reason string `json:"reason"`
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

	// games are the games the plan covers, for the apply's re-plan.
	games []*domain.Game
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
}

// CacheKeepPreviousVersions is cache.keep_previous_versions: previous
// versions kept per mod, or -1 for unlimited. It is the value config.yaml
// held when this Service opened, or the one SetCacheKeepPreviousVersions
// wrote since - never a fresh read, as no setting is mid-flow.
func (s *Service) CacheKeepPreviousVersions() int {
	if s.cacheKeepSet.Load() {
		return int(s.cacheKeep.Load())
	}
	if s.config == nil {
		return config.DefaultCacheKeepPreviousVersions
	}
	return s.config.CacheKeepPreviousVersions()
}

// SetCacheKeepPreviousVersions writes config.yaml's
// cache.keep_previous_versions, keeping every other setting, and makes it
// this Service's: the web UI's setting. Any negative count is unlimited.
// Like SetAutoUpdate it is a config.yaml write, not a mutation of anything
// a flow touches, so it takes no mutation slot; lockConfig keeps it from
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
	s.cacheKeep.Store(int64(cfg.CacheKeepPreviousVersions()))
	s.cacheKeepSet.Store(true)
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
// each with every configured game that shares it.
func (s *Service) cacheLocations(games []*domain.Game) []cacheLocation {
	known := s.ListGames()
	seen := map[string]bool{}
	sorted := slices.Clone(games)
	slices.SortFunc(sorted, func(a, b *domain.Game) int { return cmp.Compare(a.ID, b.ID) })
	var out []cacheLocation
	for _, g := range sorted {
		path := s.cacheRootOf(g)
		if seen[path] {
			continue
		}
		seen[path] = true
		loc := cacheLocation{path: path, game: g, sharing: []*domain.Game{g}}
		for _, other := range known {
			if other.ID != g.ID && s.cacheRootOf(other) == path {
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

// pruneRefs is everything that keeps a version, for one cache location,
// keyed by refKey.
type pruneRefs struct {
	installed map[string]bool
	protected map[string]bool
	// names and ids are each mod container's display name and identity,
	// as an installed row or ref records them.
	names map[string]string
	ids   map[string][2]string
}

// refKey keys a version of the mod in container modDir.
func refKey(modDir, version string) string { return modDir + "\x00" + version }

// snapshotRefs is the part of a snapshot document a prune reads.
type snapshotRefs struct {
	ProfileDocument *domain.ExportedProfile `json:"profile_document"`
	Installed       []domain.InstalledMod   `json:"installed"`
}

// readPruneRefs reads everything that keeps a version in loc - every
// sharing game's installed rows, profiles and snapshots - with changes
// applied over the rows and refs they replace. Any read that fails is an
// error: the location is then left whole.
func (s *Service) readPruneRefs(ctx context.Context, loc cacheLocation, changes []pruneChange) (*pruneRefs, error) {
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
	gameIDs := map[string]bool{}
	for _, g := range loc.sharing {
		gameIDs[g.ID] = true
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := s.db.GameVersionRows(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if isChanged(g.ID, row.Profile, row.SourceID, row.ModID) {
				note(row.SourceID, row.ModID, row.Name)
				continue
			}
			dir := note(row.SourceID, row.ModID, row.Name)
			install(dir, row.Version)
			protect(dir, row.PreviousVersion)
		}
		names, err := config.ListProfiles(s.configDir, g.ID)
		if err != nil {
			return nil, fmt.Errorf("listing the profiles of %s: %w", g.ID, err)
		}
		for _, name := range names {
			profile, err := config.LoadProfile(s.configDir, g.ID, name)
			if err != nil {
				return nil, fmt.Errorf("reading profile %s of %s: %w", name, g.ID, err)
			}
			for _, ref := range profile.Mods {
				dir := note(ref.SourceID, ref.ModID, "")
				if !isChanged(g.ID, name, ref.SourceID, ref.ModID) {
					protect(dir, ref.Version)
				}
			}
		}
		if err := s.readSnapshotRefs(ctx, g.ID, func(sourceID, modID, version string) {
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
// entry of it, and when the newest was last written.
type cachedVersion struct {
	version string
	entries []cache.ScannedEntry
	at      time.Time
}

// keeps reports whether set names v, or - for a legacy entry whose
// directory name may spell a version and a variant - the version before
// the variant separator.
func keeps(set map[string]bool, modDir string, v cachedVersion) bool {
	if set[refKey(modDir, v.version)] {
		return true
	}
	for _, e := range v.entries {
		if i := strings.LastIndex(e.Version, cache.VariantSeparator); e.Legacy && i > 0 && set[refKey(modDir, e.Version[:i])] {
			return true
		}
	}
	return false
}

// locationScan is one location's cache as a prune sees it.
type locationScan struct {
	candidates []CachePruneEntry
	versions   int // candidate versions
	mods       int // containers holding entries
	all        int // versions cached
	skipped    string
}

// scanLocation finds the entries a prune of loc removes, keeping keep
// previous versions per mod, for the mod containers only names (nil: all).
// A location whose references cannot be read is returned skipped, with no
// candidates.
func (s *Service) scanLocation(ctx context.Context, loc cacheLocation, keep int, only map[string]bool, changes []pruneChange) (locationScan, error) {
	var out locationScan
	mods, err := cache.ScanRoot(loc.path)
	if err != nil {
		out.skipped = err.Error()
		return out, nil
	}
	var refs *pruneRefs
	if keep >= 0 {
		if refs, err = s.readPruneRefs(ctx, loc, changes); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return out, ctxErr
			}
			out.skipped = err.Error()
			refs = nil
		}
	}
	for _, m := range mods {
		if strings.HasPrefix(m.Dir, domain.SourceMerged+"-") || len(m.Entries) == 0 {
			continue
		}
		if only != nil && !only[m.Dir] {
			continue
		}
		versions := groupVersions(m.Entries)
		out.mods++
		out.all += len(versions)
		if refs == nil {
			continue
		}
		previous := 0
		for _, v := range versions {
			if keeps(refs.installed, m.Dir, v) {
				continue
			}
			previous++
			if previous <= keep || keeps(refs.protected, m.Dir, v) {
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

// groupVersions groups a container's entries by version, newest first:
// by when the version's newest entry was written, then by version order,
// then by name.
func groupVersions(entries []cache.ScannedEntry) []cachedVersion {
	byVersion := map[string]*cachedVersion{}
	var order []*cachedVersion
	for _, e := range entries {
		v := byVersion[e.Version]
		if v == nil {
			v = &cachedVersion{version: e.Version}
			byVersion[e.Version] = v
			order = append(order, v)
		}
		v.entries = append(v.entries, e)
		if e.ModTime.After(v.at) {
			v.at = e.ModTime
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
			Version: e.Version, Variant: e.Variant, Bytes: bytes, path: e.Path,
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
// removed.
func (s *Service) PlanCachePrune(ctx context.Context, games []*domain.Game) (*CachePrunePlan, error) {
	keep := s.CacheKeepPreviousVersions()
	targets := s.pruneTargets(games)
	plan := &CachePrunePlan{KeepPreviousVersions: keep, Games: []string{}, Entries: []CachePruneEntry{}, games: targets}
	for _, g := range targets {
		plan.Games = append(plan.Games, g.ID)
	}
	slices.Sort(plan.Games)
	if keep < 0 {
		return plan, nil
	}
	for _, loc := range s.cacheLocations(targets) {
		scan, err := s.scanLocation(ctx, loc, keep, nil, nil)
		if err != nil {
			return nil, err
		}
		if scan.skipped != "" {
			plan.Skipped = append(plan.Skipped, CachePruneSkip{GameID: loc.game.ID, Reason: scan.skipped})
			continue
		}
		plan.Entries = append(plan.Entries, scan.candidates...)
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
	current, err := s.PlanCachePrune(ctx, plan.games)
	if err != nil {
		return nil, err
	}
	if !samePruneEntries(plan.Entries, current.Entries) {
		return nil, fmt.Errorf("%w: what the cache prune would remove has changed since it was planned", ErrStalePlan)
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
		versions[e.GameID+"\x00"+domain.ModKey(e.SourceID, e.ModID)+"\x00"+e.Version] = true
	}
	out.Versions = len(versions)
	return out
}

// CacheStatus reports games' caches - every configured game's when games
// is empty: each location's size, and what a prune would reclaim. It
// reads every cached file's size, so it costs a walk of the cache.
func (s *Service) CacheStatus(ctx context.Context, games []*domain.Game) (*CacheStatus, error) {
	keep := s.CacheKeepPreviousVersions()
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
		scan, err := s.scanLocation(ctx, loc, keep, nil, nil)
		if err != nil {
			return nil, err
		}
		row.Mods, row.Versions = scan.mods, scan.all
		if scan.skipped != "" && row.Skipped == "" {
			row.Skipped = scan.skipped
		}
		row.ReclaimableVersions = scan.versions
		for _, e := range scan.candidates {
			row.ReclaimableBytes += e.Bytes
		}
		status.Games = append(status.Games, row)
		status.Bytes += row.Bytes
		status.ReclaimableBytes += row.ReclaimableBytes
		status.ReclaimableVersions += row.ReclaimableVersions
	}
	return status, nil
}

// modDirsOf is the containers of mods, as a scan's filter.
func modDirsOf(mods []domain.ModReference) map[string]bool {
	out := make(map[string]bool, len(mods))
	for _, m := range mods {
		out[cache.ModDirName(m.SourceID, m.ModID)] = true
	}
	return out
}

// predictCachePrune is what the prune that follows a flow's changes would
// free, for the flow's plan: nil when it frees nothing, when pruning is
// off, or when the prediction cannot be made (the flow's own prune then
// decides for itself). Called outside any mutation slot; it only reads.
func (s *Service) predictCachePrune(ctx context.Context, game *domain.Game, changes []pruneChange) *CachePruneSummary {
	if len(changes) == 0 {
		return nil
	}
	keep := s.CacheKeepPreviousVersions()
	if keep < 0 {
		return nil
	}
	mods := make([]domain.ModReference, len(changes))
	for i, c := range changes {
		mods[i] = domain.ModReference{SourceID: c.SourceID, ModID: c.ModID}
	}
	var entries []CachePruneEntry
	for _, loc := range s.cacheLocations([]*domain.Game{game}) {
		scan, err := s.scanLocation(ctx, loc, keep, modDirsOf(mods), changes)
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
// slot. It never fails the flow: what it could not do comes back as
// warnings, and a cache it could not judge is left whole. nil when it
// removed nothing.
//
// It runs once the flow's work is done and is bounded by the mods the flow
// changed, so it does not stop for the flow's cancellation: a cancelled
// flow has already returned before reaching it, and one that finished
// finishes its housekeeping too.
func (s *Service) pruneAfterFlow(ctx context.Context, game *domain.Game, mods []domain.ModReference, scope Scope, sink EventSink) (*CachePruneSummary, []string) {
	if len(mods) == 0 {
		return nil, nil
	}
	ctx = context.WithoutCancel(ctx)
	keep := s.CacheKeepPreviousVersions()
	if keep < 0 {
		return nil, nil
	}
	var removed []CachePruneEntry
	var warnings []string
	for _, loc := range s.cacheLocations([]*domain.Game{game}) {
		scan, err := s.scanLocation(ctx, loc, keep, modDirsOf(mods), nil)
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
