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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
}

// SourceIndexListing is `lmm source index --all`'s document.
type SourceIndexListing struct {
	Indexes []SourceIndexEntry `json:"indexes"`
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
	listing := &SourceIndexListing{Indexes: []SourceIndexEntry{}, Warnings: []string{}}
	for _, src := range inventories {
		uses := indexUses(src, games)
		cached, err := src.CachedIndexes(ctx)
		if err != nil {
			listing.Warnings = append(listing.Warnings, fmt.Sprintf("source %s: %v", src.ID(), err))
			continue
		}
		seen := map[string]bool{}
		for _, ci := range cached {
			seen[ci.GameID] = true
			listing.Indexes = append(listing.Indexes, SourceIndexEntry{
				Source: src.ID(), Game: ci.GameID, Cached: true, Present: ci.Present,
				Packages: ci.Packages, FetchedAt: ci.FetchedAt, Bytes: ci.Bytes,
				MappedBy: uses.mappedBy(ci.GameID),
			})
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
	// A games.yaml that is not there at all reads as "no games", which is
	// not the same claim as "no game uses these": a mistyped config
	// directory looks exactly like it. Only --all goes past that.
	noGamesFile := ""
	gamesPath := filepath.Join(s.configDir, "games.yaml")
	if _, statErr := os.Stat(gamesPath); errors.Is(statErr, fs.ErrNotExist) {
		noGamesFile = fmt.Sprintf("there is no %s, so lmm cannot tell whether this index is in use", gamesPath)
	}
	games := make([]*domain.Game, 0, len(gameMap))
	for _, g := range gameMap {
		games = append(games, g)
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
		for _, ci := range cached {
			entry := IndexPruneEntry{
				Source: src.ID(), Game: ci.GameID, Bytes: ci.Bytes, FetchedAt: ci.FetchedAt,
				MappedBy: uses.mappedBy(ci.GameID),
			}
			remove, reason := pruneDecision(ci, uses, entry.MappedBy, opts.All, now)
			if remove && !opts.All && noGamesFile != "" {
				remove, reason = false, noGamesFile
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

func (u indexUsage) identifiers() []string {
	ids := make([]string, 0, len(u.byID))
	for id := range u.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
