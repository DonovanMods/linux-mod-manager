package core_test

// `lmm source index --all` and `lmm source index prune` (#410): what core
// lists and what it decides to remove. The removal MECHANISM is the
// source's (and is tested there, symlinks and all); these tests are the
// POLICY - design §2.8 held to the project's fail-closed rule.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inventorySource is an indexed source with directories on "disk".
type inventorySource struct {
	*validatingIndexedSource
	cached    map[string]source.CachedIndex
	listErr   error
	removeErr map[string]error
	removed   []string
	// ifFetchedAt records the precondition each removal was asked under.
	ifFetchedAt map[string]time.Time
}

func (s *inventorySource) CachedIndexes(context.Context) ([]source.CachedIndex, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]source.CachedIndex, 0, len(s.cached))
	for _, ci := range s.cached {
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

func (s *inventorySource) RemoveIndex(_ context.Context, id string, ifFetchedAt time.Time) (int64, error) {
	if s.ifFetchedAt == nil {
		s.ifFetchedAt = map[string]time.Time{}
	}
	s.ifFetchedAt[id] = ifFetchedAt
	if err := s.removeErr[id]; err != nil {
		return 0, err
	}
	ci, ok := s.cached[id]
	if !ok {
		return 0, nil
	}
	delete(s.cached, id)
	s.removed = append(s.removed, id)
	return ci.Bytes, nil
}

const day = 24 * time.Hour

// newPruneService: one inventory source ("ts"), three games - two mapping
// it to communities that are cached, one mapping it to a community that is
// not - and four cached directories between them.
func newPruneService(t *testing.T) (*core.Service, *inventorySource, string) {
	t.Helper()
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	now := time.Now()
	src := &inventorySource{
		validatingIndexedSource: &validatingIndexedSource{indexedSource: newIndexedSource("ts")},
		cached: map[string]source.CachedIndex{
			"fresh":   {GameID: "fresh", Present: true, Packages: 10, Bytes: 100, FetchedAt: now.Add(-2 * day), Removable: true},
			"old":     {GameID: "old", Present: true, Packages: 20, Bytes: 200, FetchedAt: now.Add(-31 * day), Removable: true},
			"unused":  {GameID: "unused", Present: true, Packages: 30, Bytes: 300, FetchedAt: now.Add(-1 * day), Removable: true},
			"foreign": {GameID: "foreign", Bytes: 400, Reason: "holds notes.txt, which is not part of an index"},
		},
	}
	svc.RegisterSource(src)
	svc.RegisterSource(newMockSource("other"))
	for _, g := range []*domain.Game{
		{ID: "alpha", SourceIDs: map[string]string{"ts": "fresh"}},
		{ID: "beta", SourceIDs: map[string]string{"ts": "old", "other": "x"}},
		{ID: "gamma", SourceIDs: map[string]string{"ts": "never-built"}},
		{ID: "delta", SourceIDs: map[string]string{"ts": "fresh"}},
	} {
		g.Name, g.ModPath, g.LinkMethod = g.ID, t.TempDir(), domain.LinkSymlink
		require.NoError(t, svc.SaveGame(t.Context(), g))
	}
	return svc, src, configDir
}

func entryFor(t *testing.T, entries []core.IndexPruneEntry, game string) core.IndexPruneEntry {
	t.Helper()
	for _, e := range entries {
		if e.Game == game {
			return e
		}
	}
	require.Failf(t, "missing", "no prune entry for %s in %+v", game, entries)
	return core.IndexPruneEntry{}
}

func TestListSourceIndexes_ListsCachedAndMappedIndexes(t *testing.T) {
	svc, _, _ := newPruneService(t)

	listing, err := svc.ListSourceIndexes(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, listing.Warnings)

	byGame := map[string]core.SourceIndexEntry{}
	for _, e := range listing.Indexes {
		assert.Equal(t, "ts", e.Source, "only a source that keeps an index is listed")
		byGame[e.Game] = e
	}
	require.Len(t, byGame, 5)
	assert.Equal(t, []string{"alpha", "delta"}, byGame["fresh"].MappedBy, "sorted, every game that maps it")
	assert.True(t, byGame["fresh"].Cached)
	assert.Equal(t, int64(100), byGame["fresh"].Bytes)
	assert.Empty(t, byGame["unused"].MappedBy)
	assert.NotNil(t, byGame["unused"].MappedBy, "an empty list, not null")
	assert.False(t, byGame["never-built"].Cached, "a mapped index with nothing on disk is listed so it can be built")
	assert.Equal(t, []string{"gamma"}, byGame["never-built"].MappedBy)
}

// TestPruneSourceIndexes_Policy is design §2.8 as a table: an index no game
// uses goes; one a game uses stays until 30 days without a refresh; one
// whose age is unknown stays; one the source cannot prove is its own stays.
func TestPruneSourceIndexes_Policy(t *testing.T) {
	svc, src, _ := newPruneService(t)

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	assert.False(t, report.DryRun)

	assert.Equal(t, core.IndexPruneRemoved, entryFor(t, report.Entries, "unused").Action)
	assert.Contains(t, entryFor(t, report.Entries, "unused").Reason, "no game uses it")
	assert.Equal(t, core.IndexPruneRemoved, entryFor(t, report.Entries, "old").Action)
	assert.Contains(t, entryFor(t, report.Entries, "old").Reason, "31 days")
	fresh := entryFor(t, report.Entries, "fresh")
	assert.Equal(t, core.IndexPruneKeep, fresh.Action)
	assert.Contains(t, fresh.Reason, "alpha")
	foreign := entryFor(t, report.Entries, "foreign")
	assert.Equal(t, core.IndexPruneKeep, foreign.Action)
	assert.Contains(t, foreign.Reason, "notes.txt")

	sort.Strings(src.removed)
	assert.Equal(t, []string{"old", "unused"}, src.removed)
	assert.Equal(t, 2, report.Removed)
	assert.Equal(t, int64(500), report.FreedBytes)
	for _, e := range report.Entries {
		assert.NotEqual(t, "never-built", e.Game, "nothing on disk, nothing to prune")
	}
}

func TestPruneSourceIndexes_AnUnknownAgeIsKept(t *testing.T) {
	svc, src, _ := newPruneService(t)
	ci := src.cached["old"]
	ci.FetchedAt = time.Time{}
	src.cached["old"] = ci

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	e := entryFor(t, report.Entries, "old")
	assert.Equal(t, core.IndexPruneKeep, e.Action)
	assert.Contains(t, e.Reason, "age")
}

// TestPruneSourceIndexes_ADryRunListsExactlyWhatTheRunRemoves: the preview
// removes nothing, and a run restricted to what it listed removes exactly
// that - which is how both frontends confirm before deleting.
func TestPruneSourceIndexes_ADryRunListsExactlyWhatTheRunRemoves(t *testing.T) {
	svc, src, _ := newPruneService(t)

	preview, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true, DryRun: true})
	require.NoError(t, err)
	assert.True(t, preview.DryRun)
	assert.Empty(t, src.removed)
	assert.Equal(t, 3, preview.Removed, "the preview counts what the run would remove")
	var planned []string
	for _, e := range preview.Entries {
		if e.Action == core.IndexPruneRemove {
			planned = append(planned, e.Game)
		}
	}
	sort.Strings(planned)
	assert.Equal(t, []string{"fresh", "old", "unused"}, planned, "--all takes a mapped, fresh index too - never an unprovable one")
	assert.Equal(t, int64(600), preview.FreedBytes, "the preview says what the run would free")

	// Something changes between the preview and the run: a new index
	// appears. The confirmed run must not take it.
	src.cached["late"] = source.CachedIndex{GameID: "late", Bytes: 7, Removable: true, FetchedAt: time.Now()}
	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true, Only: preview.RemovalKeys()})
	require.NoError(t, err)
	sort.Strings(src.removed)
	assert.Equal(t, planned, src.removed)
	late := entryFor(t, report.Entries, "late")
	assert.Equal(t, core.IndexPruneKeep, late.Action)
	assert.Contains(t, late.Reason, "not in the list")
}

// TestPruneSourceIndexes_AGamesFileThatWillNotParseRemovesNothing: without
// games.yaml lmm cannot tell what is in use, so it removes nothing at all -
// not even with --all, which is still a claim about what the user has.
func TestPruneSourceIndexes_AGamesFileThatWillNotParseRemovesNothing(t *testing.T) {
	for _, all := range []bool{false, true} {
		svc, src, configDir := newPruneService(t)
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte("games: [this is: not: a map"), 0o644))

		_, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: all})
		require.Error(t, err, "all=%v", all)
		assert.Contains(t, err.Error(), "games.yaml")
		assert.Empty(t, src.removed, "all=%v", all)
	}
}

// TestPruneSourceIndexes_AnUnresolvableMappingKeepsUnusedIndexes: a game
// that maps the source to nothing (or to something the source rejects)
// wants SOME index lmm cannot name, so no index can be proved unused.
func TestPruneSourceIndexes_AnUnresolvableMappingKeepsUnusedIndexes(t *testing.T) {
	for _, value := range []string{"", "Not A Slug"} {
		svc, src, _ := newPruneService(t)
		require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
			ID: "epsilon", Name: "epsilon", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
			SourceIDs: map[string]string{"ts": value},
		}))

		report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
		require.NoError(t, err)
		unused := entryFor(t, report.Entries, "unused")
		assert.Equal(t, core.IndexPruneKeep, unused.Action, "mapping %q", value)
		assert.Contains(t, unused.Reason, "epsilon")
		assert.Equal(t, core.IndexPruneRemoved, entryFor(t, report.Entries, "old").Action,
			"a mapped index past its age is still provably removable")
		assert.NotContains(t, src.removed, "unused")
	}
}

func TestPruneSourceIndexes_AnInventoryFailureIsAWarningNotARemoval(t *testing.T) {
	svc, src, _ := newPruneService(t)
	src.listErr = errors.New("_thunderstore is a symbolic link")

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true})
	require.NoError(t, err)
	assert.Empty(t, report.Entries)
	require.Len(t, report.Warnings, 1)
	assert.Contains(t, report.Warnings[0], "symbolic link")
	assert.Empty(t, src.removed)
}

func TestPruneSourceIndexes_ARemovalThatFailsIsReportedAndTheRestContinue(t *testing.T) {
	svc, src, _ := newPruneService(t)
	src.removeErr = map[string]error{"old": errors.New("holds sub, which is not a plain file lmm wrote")}

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	old := entryFor(t, report.Entries, "old")
	assert.Equal(t, core.IndexPruneFailed, old.Action)
	assert.Contains(t, old.Reason, "holds sub")
	assert.Equal(t, core.IndexPruneRemoved, entryFor(t, report.Entries, "unused").Action)
	assert.Equal(t, 1, report.Removed)
	assert.Equal(t, int64(300), report.FreedBytes)
}

func TestPruneSourceIndexes_ScopesToOneSource(t *testing.T) {
	svc, src, _ := newPruneService(t)
	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{SourceID: "other"})
	require.Error(t, err, "a source that keeps no index has nothing to prune")
	assert.ErrorIs(t, err, source.ErrNotSupported)
	assert.Nil(t, report)
	assert.Empty(t, src.removed)
}

// TestPruneSourceIndexes_AnAgeDecisionTravelsToTheRemoval: a removal decided
// on an index's age carries that age as its precondition, so a refresh in
// between keeps the index. An unused index's age pins WHICH copy was judged
// unused the same way; only --all, which removes whatever is there, asks for
// no such check.
func TestPruneSourceIndexes_AnAgeDecisionTravelsToTheRemoval(t *testing.T) {
	svc, src, _ := newPruneService(t)
	oldAt, unusedAt := src.cached["old"].FetchedAt, src.cached["unused"].FetchedAt

	_, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	assert.Equal(t, oldAt, src.ifFetchedAt["old"], "the 30-day decision is re-checked under the lock")
	assert.Equal(t, unusedAt, src.ifFetchedAt["unused"], "and so is which copy was judged unused")

	svc, src, _ = newPruneService(t)
	_, err = svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true})
	require.NoError(t, err)
	for id, at := range src.ifFetchedAt {
		assert.True(t, at.IsZero(), "--all removes %s whatever its age", id)
	}
}

// TestPruneSourceIndexes_NoGamesFileKeepsUnusedIndexes: a games.yaml that is
// not there is not proof that no game uses anything - a mistyped config
// directory looks exactly like it - so only --all removes anything then.
func TestPruneSourceIndexes_NoGamesFileKeepsUnusedIndexes(t *testing.T) {
	svc, src, configDir := newPruneService(t)
	require.NoError(t, os.Remove(filepath.Join(configDir, "games.yaml")))

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	assert.Empty(t, src.removed)
	unused := entryFor(t, report.Entries, "unused")
	assert.Equal(t, core.IndexPruneKeep, unused.Action)
	assert.Contains(t, unused.Reason, "games.yaml")

	report, err = svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true})
	require.NoError(t, err)
	assert.Len(t, src.removed, 3, "--all is the explicit way past it")
	assert.Equal(t, 3, report.Removed)
}

// TestPruneSourceIndexes_AGamesFileLmmCannotFullyReadKeepsEverything is T3
// review F4: games.yaml is decoded leniently, so a file that is empty, has
// a mistyped key, or has a game indented out of its block reads as FEWER
// games than it holds - and every index those games use looked unused. Any
// doubt about the file keeps every index, and says why; only --all, which
// removes whatever a source can prove is its own, goes past it.
func TestPruneSourceIndexes_AGamesFileLmmCannotFullyReadKeepsEverything(t *testing.T) {
	alpha := "games:\n  alpha:\n    name: alpha\n    mod_path: /tmp/alpha\n    sources:\n      ts: fresh\n"
	for name, content := range map[string]string{
		"empty":               "",
		"only a comment":      "# games go here\n",
		"games with no value": "games:\n",
		"a mistyped key":      "game:\n  alpha:\n    sources:\n      ts: fresh\n",
		"a game out of its block": alpha +
			"beta:\n  name: beta\n  sources:\n    ts: unused\n",
		"a key out of its game":   alpha + "    name2: x\n",
		"a game with no settings": "games:\n  alpha:\n",
	} {
		t.Run(name, func(t *testing.T) {
			svc, src, configDir := newPruneService(t)
			require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(content), 0o644))

			report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
			require.NoError(t, err)
			assert.Empty(t, src.removed)
			for _, e := range report.Entries {
				assert.Equal(t, core.IndexPruneKeep, e.Action, "%s: %+v", e.Game, e)
			}
			unused := entryFor(t, report.Entries, "unused")
			assert.Contains(t, unused.Reason, "games.yaml")

			_, err = svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true})
			require.NoError(t, err)
			assert.Len(t, src.removed, 3, "--all is the explicit way past it")
		})
	}
}

// TestPruneSourceIndexes_AGamesFileMissingAGameWithProfilesKeepsEverything:
// a games.yaml cut off at a game boundary (#403's non-atomic writer) parses
// cleanly with fewer games. A game whose profiles are still on disk but
// which games.yaml no longer lists is that doubt, made visible.
func TestPruneSourceIndexes_AGamesFileMissingAGameWithProfilesKeepsEverything(t *testing.T) {
	svc, src, configDir := newPruneService(t)
	require.NoError(t, os.MkdirAll(filepath.Join(configDir, "games", "zeta", "profiles"), 0o755))

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	assert.Empty(t, src.removed)
	unused := entryFor(t, report.Entries, "unused")
	assert.Equal(t, core.IndexPruneKeep, unused.Action)
	assert.Contains(t, unused.Reason, "zeta")
}

// TestPruneSourceIndexes_TheGamesThisProcessLoadedStillCount: the games the
// Service read at start are a second witness to what is mapped - a file
// re-read mid-write must not be the only one.
func TestPruneSourceIndexes_TheGamesThisProcessLoadedStillCount(t *testing.T) {
	svc, src, configDir := newPruneService(t)
	require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
		ID: "epsilon", Name: "epsilon", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"ts": "unused"},
	}))
	// games.yaml loses epsilon behind the Service's back, and still parses.
	data, err := os.ReadFile(filepath.Join(configDir, "games.yaml"))
	require.NoError(t, err)
	trimmed := strings.Split(string(data), "    epsilon:")[0]
	require.NotEqual(t, string(data), trimmed, "fixture: epsilon was written")
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(trimmed), 0o644))

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	unused := entryFor(t, report.Entries, "unused")
	assert.Equal(t, core.IndexPruneKeep, unused.Action)
	assert.Equal(t, []string{"epsilon"}, unused.MappedBy)
	assert.NotContains(t, src.removed, "unused")
}

// holdingSource is an inventory source that is holding requests off.
type holdingSource struct {
	*inventorySource
	holds []source.Hold
}

func (s *holdingSource) Holds(context.Context) []source.Hold { return s.holds }

// TestListSourceIndexes_ListsTheHoldsInForce is T3 review F10: the web UI's
// setup card shows when lmm will next ask Thunderstore without asking it.
func TestListSourceIndexes_ListsTheHoldsInForce(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	at := time.Date(2026, 9, 16, 12, 10, 0, 0, time.UTC)
	svc.RegisterSource(&holdingSource{
		inventorySource: &inventorySource{validatingIndexedSource: &validatingIndexedSource{indexedSource: newIndexedSource("ts")}},
		holds: []source.Hold{
			{Source: "Thunderstore", Until: at, Reason: "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s"},
			{Source: "Thunderstore", GameID: "broken", Until: at.Add(time.Minute), Reason: "suspended after 3 failed requests in a row"},
		},
	})

	listing, err := svc.ListSourceIndexes(t.Context(), "")
	require.NoError(t, err)
	assert.Equal(t, []core.IndexHold{
		{Source: "ts", RetryAt: at, Reason: "rate limited by Thunderstore (HTTP 429), which asked lmm to wait 10m0s"},
		{Source: "ts", Game: "broken", RetryAt: at.Add(time.Minute), Reason: "suspended after 3 failed requests in a row"},
	}, listing.Holds)

	empty, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, empty.Close()) })
	listing, err = empty.ListSourceIndexes(t.Context(), "")
	require.NoError(t, err)
	assert.NotNil(t, listing.Holds, "an empty list, not null")
}

// TestListSourceIndexes_SaysWhyAPruneWouldKeepAnIndex: an index the source
// cannot prove is its own - a stranger in the directory, a symbolic link
// above it - is listed with the reason, so the listing does not promise a
// prune it will not make (review F7).
func TestListSourceIndexes_SaysWhyAPruneWouldKeepAnIndex(t *testing.T) {
	svc, _, _ := newPruneService(t)
	listing, err := svc.ListSourceIndexes(t.Context(), "")
	require.NoError(t, err)
	for _, e := range listing.Indexes {
		switch e.Game {
		case "foreign":
			assert.Equal(t, "holds notes.txt, which is not part of an index", e.KeepReason)
		default:
			assert.Empty(t, e.KeepReason, e.Game)
		}
	}
}

// TestListSourceIndexes_AnInventoryFailureStillListsMappedIndexes: a source
// that cannot list its directories still has the indexes its games map
// (review F7: they vanished with the warning).
func TestListSourceIndexes_AnInventoryFailureStillListsMappedIndexes(t *testing.T) {
	svc, src, _ := newPruneService(t)
	src.listErr = errors.New("reading the index root: permission denied")

	listing, err := svc.ListSourceIndexes(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, listing.Warnings, 1)
	var games []string
	for _, e := range listing.Indexes {
		games = append(games, e.Game)
		assert.False(t, e.Cached)
	}
	assert.Equal(t, []string{"fresh", "never-built", "old"}, games)
}

// TestPruneSourceIndexes_AGamesFileCutShortMidSlugKeepsTheIndex (#468): a
// games.yaml a non-atomic write (#403) cut off in the middle of a mapped
// community slug still parses, and a shortened slug is still a valid one -
// so the game appears to map a community with no index, and the index it
// really uses looked unused. An index whose name extends a mapped slug that
// has no index of its own is kept, with the reason; --all still removes it.
func TestPruneSourceIndexes_AGamesFileCutShortMidSlugKeepsTheIndex(t *testing.T) {
	for name, cut := range map[string]string{
		"cut mid-word":         "lethal-com",
		"cut after the hyphen": "lethal-",
	} {
		t.Run(name, func(t *testing.T) {
			svc, src, configDir := newPruneService(t)
			src.cached["lethal-company"] = source.CachedIndex{
				GameID: "lethal-company", Present: true, Bytes: 35 << 20,
				FetchedAt: time.Now().Add(-day), Removable: true,
			}
			require.NoError(t, svc.SaveGame(t.Context(), &domain.Game{
				ID: "lc", Name: "lc", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
				SourceIDs: map[string]string{"ts": "lethal-company"},
			}))

			// The file on disk is cut inside the slug, and a later lmm
			// process - which never saw the whole file - prunes.
			path := filepath.Join(configDir, "games.yaml")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			i := strings.Index(string(data), "lethal-company")
			require.Positive(t, i)
			require.NoError(t, os.WriteFile(path, []byte(string(data)[:i]+cut+string(data)[i+len("lethal-company"):]), 0o644))
			later, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, later.Close()) })
			later.RegisterSource(src)
			later.RegisterSource(newMockSource("other"))

			report, err := later.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
			require.NoError(t, err)
			kept := entryFor(t, report.Entries, "lethal-company")
			assert.Equal(t, core.IndexPruneKeep, kept.Action)
			assert.Contains(t, kept.Reason, `game lc maps this source to "`+cut+`"`)
			assert.Contains(t, kept.Reason, "cut short")
			assert.NotContains(t, src.removed, "lethal-company")
			assert.Contains(t, src.removed, "unused", "an index nothing extends still goes")

			_, err = later.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{All: true})
			require.NoError(t, err)
			assert.Contains(t, src.removed, "lethal-company", "--all is the explicit way past it")
		})
	}
}

// TestPruneSourceIndexes_AMappedSlugWithItsOwnIndexDoesNotKeepLongerOnes:
// the #468 doubt needs the short slug to have no index - "old" is cached
// and mapped, so an unused "older" is not a truncation of it.
func TestPruneSourceIndexes_AMappedSlugWithItsOwnIndexDoesNotKeepLongerOnes(t *testing.T) {
	svc, src, _ := newPruneService(t)
	src.cached["older"] = source.CachedIndex{GameID: "older", Present: true, Bytes: 1, FetchedAt: time.Now().Add(-day), Removable: true}

	report, err := svc.PruneSourceIndexes(t.Context(), core.IndexPruneOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.IndexPruneRemoved, entryFor(t, report.Entries, "older").Action)
}
