// Package core: this file is the INVENTORY half of the local-index surface
// (#410): listing every index a source keeps on disk, and pruning the ones
// nobody needs.
//
// The design put this under `lmm cache info`/`prune` (§2.8), a command lmm
// does not have; it lives beside the rest of the index surface instead
// (`lmm source index --all`, `lmm source index prune`). The POLICY is here;
// the deleting is the source's (source.IndexInventory), which refuses
// anything it cannot prove is its own. Both halves fail closed: any doubt -
// an unreadable games.yaml, a game whose identifier lmm cannot resolve, a
// directory with a stranger in it - keeps the index.
package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// IndexPruneMaxAge is how long an index a game still uses may go without a
// refresh before a prune removes it anyway (design §2.8): long enough that
// a game played monthly keeps its index, short enough that one abandoned
// without being unmapped does not hold 220 MB forever.
const IndexPruneMaxAge = 30 * 24 * time.Hour

// The IndexPruneEntry.Action vocabulary.
const (
	// IndexPruneRemove: a dry run would remove this index.
	IndexPruneRemove = "remove"
	// IndexPruneRemoved: this run removed it.
	IndexPruneRemoved = "removed"
	// IndexPruneKeep: the index stays; Reason says why.
	IndexPruneKeep = "keep"
	// IndexPruneFailed: the index was due for removal and the source
	// refused or failed; nothing of it was removed, and Reason says why.
	IndexPruneFailed = "failed"
)

// SourceIndexEntry is one index as `lmm source index --all` lists it: a
// directory a source keeps on disk, or an index a game maps that has never
// been built.
type SourceIndexEntry struct {
	Source string `json:"source"`
	// Game is the SOURCE's identifier - a Thunderstore community slug.
	Game string `json:"game"`
	// Cached reports whether a directory for it exists on disk at all.
	Cached    bool      `json:"cached"`
	Present   bool      `json:"present"`
	Packages  int       `json:"packages"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Bytes     int64     `json:"bytes"`
	// MappedBy is every lmm game whose games.yaml maps the source to this
	// identifier, sorted; empty when no game uses it.
	MappedBy []string `json:"mapped_by"`
	// KeepReason, when set, is why a prune would keep this index whatever
	// its use or age: the source cannot prove the directory is only its own
	// index (T3 review F7).
	KeepReason string `json:"keep_reason,omitempty"`
}

// IndexHold is a source that will not be asked - or, with Game set, will
// not be asked about one index - before RetryAt (T3 review F10): a wait the
// host named, or a breaker lmm tripped after repeated failures.
type IndexHold struct {
	Source string `json:"source"`
	// Game is the source's identifier for the one index held; empty holds
	// every index of the source.
	Game    string    `json:"game,omitempty"`
	RetryAt time.Time `json:"retry_at"`
	Reason  string    `json:"reason"`
}

// SourceIndexListing is `lmm source index --all`'s document.
type SourceIndexListing struct {
	Indexes []SourceIndexEntry `json:"indexes"`
	// Holds is every hold in force, so a frontend can say when lmm will
	// next ask a host without asking it.
	Holds []IndexHold `json:"holds"`
	// Warnings names a source whose indexes could not be listed at all.
	Warnings []string `json:"warnings"`
}

// IndexPruneOptions selects what PruneSourceIndexes removes.
type IndexPruneOptions struct {
	// SourceID limits the prune to one source; empty means every source
	// that keeps an index.
	SourceID string
	// All removes every index a source can prove is its own, whether or not
	// a game uses it and however recently it was refreshed.
	All bool
	// DryRun decides and reports without removing anything.
	DryRun bool
	// Only, when non-nil, restricts removal to these IndexPruneKey values -
	// the ones a dry run listed and the user confirmed - so a confirmed run
	// can never remove more than was shown.
	Only []string
}

// IndexPruneEntry is one cached index and what the prune did with it.
type IndexPruneEntry struct {
	Source    string    `json:"source"`
	Game      string    `json:"game"`
	Bytes     int64     `json:"bytes"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	MappedBy  []string  `json:"mapped_by"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
}

// IndexPruneReport is `lmm source index prune`'s document.
type IndexPruneReport struct {
	DryRun  bool              `json:"dry_run"`
	All     bool              `json:"all"`
	Entries []IndexPruneEntry `json:"entries"`
	// Removed and FreedBytes count what this run removed - or, for a dry
	// run, what it would.
	Removed    int      `json:"removed"`
	FreedBytes int64    `json:"freed_bytes"`
	Warnings   []string `json:"warnings"`
}

// IndexPruneKey names one index for IndexPruneOptions.Only.
func IndexPruneKey(sourceID, game string) string { return sourceID + "/" + game }

// RemovalKeys is the IndexPruneOptions.Only list that confirms exactly the
// removals this report planned.
func (r *IndexPruneReport) RemovalKeys() []string {
	keys := []string{}
	for _, e := range r.Entries {
		if e.Action == IndexPruneRemove || e.Action == IndexPruneRemoved {
			keys = append(keys, IndexPruneKey(e.Source, e.Game))
		}
	}
	return keys
}

// ListSourceIndexes lists every index the sources keep on disk, plus every
// index a game maps that has not been built yet, with the games that use
// each. sourceID limits it to one source; "" lists them all. A READ.
func (s *Service) ListSourceIndexes(ctx context.Context, sourceID string) (*SourceIndexListing, error) {
	inventories, err := s.indexInventories(sourceID)
	if err != nil {
		return nil, err
	}
	games := s.gamesSnapshot()
	listing := &SourceIndexListing{Indexes: []SourceIndexEntry{}, Holds: []IndexHold{}, Warnings: []string{}}
	for _, src := range inventories {
		uses := indexUses(src, games)
		listing.Holds = append(listing.Holds, indexHolds(ctx, src)...)
		cached, err := src.CachedIndexes(ctx)
		if err != nil {
			// What is on disk is unknown; what the games map is not, and
			// dropping those rows too left an empty table under the
			// warning (T3 review F7).
			listing.Warnings = append(listing.Warnings, fmt.Sprintf("source %s: %v", src.ID(), err))
		}
		seen := map[string]bool{}
		for _, ci := range cached {
			seen[ci.GameID] = true
			entry := SourceIndexEntry{
				Source: src.ID(), Game: ci.GameID, Cached: true, Present: ci.Present,
				Packages: ci.Packages, FetchedAt: ci.FetchedAt, Bytes: ci.Bytes,
				MappedBy: uses.mappedBy(ci.GameID),
			}
			if !ci.Removable {
				entry.KeepReason = ci.Reason
			}
			listing.Indexes = append(listing.Indexes, entry)
		}
		for _, id := range uses.identifiers() {
			if !seen[id] {
				listing.Indexes = append(listing.Indexes, SourceIndexEntry{
					Source: src.ID(), Game: id, MappedBy: uses.mappedBy(id),
				})
			}
		}
	}
	sort.SliceStable(listing.Indexes, func(i, j int) bool {
		a, b := listing.Indexes[i], listing.Indexes[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Game < b.Game
	})
	return listing, nil
}

// PruneSourceIndexes removes the cached indexes nobody needs (design §2.8):
// any index no game maps, and a mapped one not refreshed for
// IndexPruneMaxAge - or, with All, every index a source can prove is its
// own. It is a MUTATION (it deletes under the cache root) unless DryRun is
// set, and takes the mutation slot like every other.
//
// It fails closed. games.yaml is re-read from disk, because "which indexes
// are in use" is a claim about the file as it is NOW, and if it cannot be
// read nothing is removed at all. A game that maps a source to an empty or
// malformed identifier wants an index lmm cannot name, so no index of that
// source counts as unused. A source that cannot list its indexes is a
// warning, not a removal. And the source itself refuses any directory it
// cannot prove is its own.
func (s *Service) PruneSourceIndexes(ctx context.Context, opts IndexPruneOptions) (*IndexPruneReport, error) {
	inventories, err := s.indexInventories(opts.SourceID)
	if err != nil {
		return nil, err
	}
	if !opts.DryRun {
		release, err := s.beginOp(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	gameMap, err := s.LoadGamesFromDisk()
	if err != nil {
		return nil, fmt.Errorf("not pruning anything: games.yaml could not be read, so lmm cannot tell which indexes are in use: %w", err)
	}
	// Any doubt that the file lmm just read names every game there is -
	// it is missing, or it holds more than lmm could read out of it (T3
	// review F4) - keeps every index. Only --all goes past it.
	doubt := s.gamesFileDoubt(gameMap)
	// The games this Service loaded are a second witness (review F4): a
	// games.yaml re-read while another lmm is rewriting it (#403) must not
	// be the only one.
	games := make([]*domain.Game, 0, len(gameMap))
	for _, g := range gameMap {
		games = append(games, g)
	}
	for _, g := range s.gamesSnapshot() {
		if _, onDisk := gameMap[g.ID]; !onDisk {
			games = append(games, g)
		}
	}

	var only map[string]bool
	if opts.Only != nil {
		only = make(map[string]bool, len(opts.Only))
		for _, k := range opts.Only {
			only[k] = true
		}
	}

	report := &IndexPruneReport{DryRun: opts.DryRun, All: opts.All, Entries: []IndexPruneEntry{}, Warnings: []string{}}
	now := time.Now()
	for _, src := range inventories {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		uses := indexUses(src, games)
		cached, err := src.CachedIndexes(ctx)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("source %s: not pruning: %v", src.ID(), err))
			continue
		}
		cachedIDs := make(map[string]bool, len(cached))
		for _, ci := range cached {
			cachedIDs[ci.GameID] = true
		}
		for _, ci := range cached {
			entry := IndexPruneEntry{
				Source: src.ID(), Game: ci.GameID, Bytes: ci.Bytes, FetchedAt: ci.FetchedAt,
				MappedBy: uses.mappedBy(ci.GameID),
			}
			remove, reason := pruneDecision(ci, uses, entry.MappedBy, opts.All, now)
			if remove && !opts.All && len(entry.MappedBy) == 0 {
				if why := uses.truncationDoubt(ci.GameID, cachedIDs); why != "" {
					remove, reason = false, why
				}
			}
			if remove && !opts.All && doubt != "" {
				remove, reason = false, doubt
			}
			if remove && only != nil && !only[IndexPruneKey(src.ID(), ci.GameID)] {
				remove, reason = false, "not in the list of indexes confirmed for removal"
			}
			// Unless --all, the decision rested on WHICH index this was -
			// its age, or the copy judged unused - so the source re-checks
			// that under its lock and refuses once a refresh has replaced it.
			var ifFetchedAt time.Time
			if !opts.All {
				ifFetchedAt = ci.FetchedAt
			}
			entry.Reason = reason
			switch {
			case !remove:
				entry.Action = IndexPruneKeep
			case opts.DryRun:
				entry.Action = IndexPruneRemove
				report.Removed++
				report.FreedBytes += ci.Bytes
			default:
				freed, err := src.RemoveIndex(ctx, ci.GameID, ifFetchedAt)
				if err != nil {
					entry.Action, entry.Reason = IndexPruneFailed, err.Error()
					break
				}
				entry.Action = IndexPruneRemoved
				report.Removed++
				report.FreedBytes += freed
			}
			report.Entries = append(report.Entries, entry)
		}
	}
	return report, nil
}

// gamesFileKeys are the keys a game's block in games.yaml may hold
// (storage/config.GameConfig). A key outside them is a typo, or another
// game's line indented into this one.
var gamesFileKeys = map[string]bool{
	"name": true, "install_path": true, "mod_path": true, "sources": true,
	"link_method": true, "cache_path": true, "hooks": true, "deploy_mode": true,
	"adapter": true, "convert_paks": true, "loader": true,
}

// gamesFileDoubt says why games.yaml, as loaded, may not name every game
// the user has - or "" when it can be trusted to. The loader decodes
// leniently, so a file that is empty, has a mistyped key, or has a game
// indented out of its block does not fail: it yields fewer games than it
// holds, and the indexes those games use looked unused (T3 review F4).
//
// So the file is read a second time, structurally: its only top-level key
// is games, that is a map of game blocks, each block holds only keys a game
// has, and there are exactly as many as were loaded. And a game whose
// profiles are on disk but which games.yaml does not list is the other
// sign - a file cut off at a game boundary parses cleanly.
func (s *Service) gamesFileDoubt(loaded map[string]*domain.Game) string {
	path := filepath.Join(s.configDir, "games.yaml")
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Not there at all reads as "no games", which is not the same
		// claim as "no game uses these": a mistyped config directory
		// looks exactly like it.
		return fmt.Sprintf("there is no %s, so lmm cannot tell whether this index is in use", path)
	case err != nil:
		return fmt.Sprintf("%s could not be read (%v), so lmm cannot tell whether this index is in use", path, err)
	}
	if why := gamesFileShapeDoubt(data, len(loaded)); why != "" {
		return fmt.Sprintf("%s %s, so lmm cannot tell whether this index is in use", path, why)
	}
	if id := unlistedGameWithProfiles(s.configDir, loaded); id != "" {
		return fmt.Sprintf("game %s has profiles on disk but %s does not list it (was the file cut short?), so lmm cannot tell whether this index is in use", id, path)
	}
	return ""
}

// gamesFileShapeDoubt checks data's structure against what lmm writes, and
// says what is wrong with it, or "".
func gamesFileShapeDoubt(data []byte, loaded int) string {
	if len(bytes.TrimSpace(data)) == 0 {
		return "is empty"
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Sprintf("does not parse (%v)", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "holds no games: section"
	}
	top := doc.Content[0]
	var games *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		key := top.Content[i].Value
		if key != "games" {
			return fmt.Sprintf("has a top-level key %q lmm does not know (a typo, or a game indented out of its block)", key)
		}
		games = top.Content[i+1]
	}
	switch {
	case games == nil:
		return "holds no games: section"
	case games.Kind != yaml.MappingNode:
		return "has a games: section that is not a list of games"
	}
	blocks := 0
	for i := 0; i+1 < len(games.Content); i += 2 {
		id, block := games.Content[i].Value, games.Content[i+1]
		if block.Kind != yaml.MappingNode {
			return fmt.Sprintf("has a game %q that is not a block of settings", id)
		}
		for j := 0; j+1 < len(block.Content); j += 2 {
			if key := block.Content[j].Value; !gamesFileKeys[key] {
				return fmt.Sprintf("has a key %q in game %q that lmm does not know (a typo, or a line indented into the wrong game)", key, id)
			}
		}
		blocks++
	}
	if blocks != loaded {
		return fmt.Sprintf("holds %d game blocks but lmm read %d games from it", blocks, loaded)
	}
	return ""
}

// unlistedGameWithProfiles names a game that has a profiles directory under
// configDir but is not in loaded, or "".
func unlistedGameWithProfiles(configDir string, loaded map[string]*domain.Game) string {
	entries, err := os.ReadDir(filepath.Join(configDir, "games"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || loaded[e.Name()] != nil {
			continue
		}
		if info, err := os.Stat(filepath.Join(configDir, "games", e.Name(), "profiles")); err == nil && info.IsDir() {
			return e.Name()
		}
	}
	return ""
}

// pruneDecision is design §2.8 for one cached index.
func pruneDecision(ci source.CachedIndex, uses indexUsage, mappedBy []string, all bool, now time.Time) (bool, string) {
	switch {
	case !ci.Removable:
		return false, ci.Reason
	case all:
		return true, "--all removes every index"
	case len(mappedBy) > 0:
		if ci.FetchedAt.IsZero() {
			return false, fmt.Sprintf("used by %s, and its age is unknown", strings.Join(mappedBy, ", "))
		}
		age := now.Sub(ci.FetchedAt)
		if age < IndexPruneMaxAge {
			return false, fmt.Sprintf("used by %s", strings.Join(mappedBy, ", "))
		}
		return true, fmt.Sprintf("used by %s, but not refreshed for %d days", strings.Join(mappedBy, ", "), int(age/(24*time.Hour)))
	case len(uses.unresolved) > 0:
		return false, fmt.Sprintf("game %s maps this source to an identifier lmm cannot use, so lmm cannot tell whether this index is in use",
			strings.Join(uses.unresolved, ", "))
	default:
		return true, "no game uses it"
	}
}

// indexUsage is which games map one source to which identifiers.
type indexUsage struct {
	byID map[string][]string
	// unresolved names the games that map the source to an empty or
	// malformed identifier.
	unresolved []string
}

func indexUses(src source.ModSource, games []*domain.Game) indexUsage {
	u := indexUsage{byID: map[string][]string{}}
	validator, _ := src.(source.GameIdentifierValidator)
	for _, g := range games {
		id, mapped := g.SourceIDs[src.ID()]
		if !mapped {
			continue
		}
		id = strings.TrimSpace(id)
		if id == "" || (validator != nil && validator.ValidateGameIdentifier(id) != nil) {
			u.unresolved = append(u.unresolved, g.ID)
			continue
		}
		u.byID[id] = append(u.byID[id], g.ID)
	}
	for id := range u.byID {
		sort.Strings(u.byID[id])
	}
	sort.Strings(u.unresolved)
	return u
}

func (u indexUsage) mappedBy(id string) []string {
	if games := u.byID[id]; len(games) > 0 {
		return append([]string(nil), games...)
	}
	return []string{}
}

// truncationDoubt says why the unmapped index id may be one a game uses
// after all, or "" (#468): a game maps the source to an identifier that is
// a strict prefix of id and has no index of its own. That is what a
// games.yaml cut short in the middle of the identifier looks like (#403's
// non-atomic write) - it still parses, and a shortened slug is still a
// valid one - so the index the game really uses would look unused.
func (u indexUsage) truncationDoubt(id string, cached map[string]bool) string {
	for _, mapped := range u.identifiers() {
		if mapped == id || cached[mapped] || !strings.HasPrefix(id, mapped) {
			continue
		}
		return fmt.Sprintf("game %s maps this source to %q, which has no index of its own and is the start of this index's name - games.yaml may have been cut short mid-name, so lmm cannot tell whether this index is in use",
			strings.Join(u.byID[mapped], ", "), mapped)
	}
	return ""
}

func (u indexUsage) identifiers() []string {
	ids := make([]string, 0, len(u.byID))
	for id := range u.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// indexHolds is every hold src has in force, as the wire shape.
func indexHolds(ctx context.Context, src source.ModSource) []IndexHold {
	reporter, ok := src.(source.HoldReporter)
	if !ok {
		return nil
	}
	var out []IndexHold
	for _, h := range reporter.Holds(ctx) {
		out = append(out, IndexHold{Source: src.ID(), Game: h.GameID, RetryAt: h.Until.UTC(), Reason: h.Reason})
	}
	return out
}

// indexInventoryOf is a registered source that can list and remove its own
// indexes.
type indexInventoryOf interface {
	source.ModSource
	source.IndexInventory
}

// indexInventories resolves sourceID (or every registered source, for "")
// to the sources that keep an index inventory. A named source that keeps
// none is source.ErrNotSupported.
func (s *Service) indexInventories(sourceID string) ([]indexInventoryOf, error) {
	if sourceID != "" {
		src, err := s.registry.Get(sourceID)
		if err != nil {
			return nil, err
		}
		inv, ok := src.(indexInventoryOf)
		if !ok {
			return nil, fmt.Errorf("source %q keeps no local index: %w", sourceID, source.ErrNotSupported)
		}
		return []indexInventoryOf{inv}, nil
	}
	var out []indexInventoryOf
	for _, src := range s.registry.List() {
		if inv, ok := src.(indexInventoryOf); ok {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}
