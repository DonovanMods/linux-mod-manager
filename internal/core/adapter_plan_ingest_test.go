package core_test

// The plan/ingest agreement property for the adapter seam (#411, I1).
//
// PlanImportArchive and the ingest each ask the game's adapter to lay the
// archive out. They can only be trusted to agree if they hand it the SAME
// request - so this file drives both halves over every archive shape in the
// identity proof's corpus, with the identity adapter AND with an adapter
// whose Layout is a function of NormalizeRequest.ModName.

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planIngestCorpus is internal/adapter/identity_test.go's importCorpus,
// minus the shapes an extract-mode import cannot carry: an empty archive
// has nothing to plan, and the single-file compile shapes are a compile
// game's business, not this seam's.
//
// It carries no BepInEx-ROOTED shape any more: #358's archive normaliser
// refuses a `BepInEx/core/` archive outright (it is the loader itself, not a
// mod) and #359 refuses any BepInEx-shaped archive imported into a game that
// declares no loader - so the shape cannot reach this seam through a plain
// game at all. adapter_loader_layout_test.go covers it where it now belongs:
// a game that DOES declare the loader, with the normaliser and the adapter
// both in play.
var planIngestCorpus = map[string][]string{
	"sole top-level directory": {"MyMod/a.esp", "MyMod/b.esp", "MyMod/sub/b.txt"},
	"flat root":                {"a.esp", "readme.txt"},
	"bethesda data root":       {"Data/mod.esp", "Data/meshes/test.nif", "Data/game.ini"},
	"bare plugins root":        {"plugins/mod.dll"},
	"nested only":              {"aaa/b.txt", "aaa/d.txt"},
	"single compile artifact":  {"MyMod_P.pak"},
	"native merge source":      {"MyMod.exmodz"},
	"no directory at all":      {"single.txt"},
}

// byNameStub renames every member under a directory named after the mod, so
// its Layout is a pure function of NormalizeRequest.ModName. The identity
// adapter cannot catch a ModName disagreement; this can.
type byNameStub struct{}

func (byNameStub) ID() string    { return "by-name" }
func (byNameStub) Label() string { return "By name" }

func (byNameStub) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	rewrites := make(map[string]string, len(req.Members))
	for _, m := range req.Members {
		rewrites[m] = "by-name/" + req.ModName + "/" + filepath.Base(m)
	}
	return adapter.NewLayout("by-name", rewrites), nil
}

// TestPlanImportArchive_AgreesWithIngestForEveryShape is I1's regression
// test: the plan built its Layout request from importedModName (the mod's
// DERIVED name, "Flat-2.0") while the ingest built its own from the archive
// filename ("Flat-2.0.zip"), so an adapter that names a directory after the
// mod planned a path the ingest never created.
func TestPlanImportArchive_AgreesWithIngestForEveryShape(t *testing.T) {
	for _, ad := range []adapter.GameAdapter{adapter.Generic{}, byNameStub{}} {
		t.Run(ad.ID(), func(t *testing.T) {
			for name, members := range planIngestCorpus {
				t.Run(name, func(t *testing.T) {
					svc, game := newImportArchiveTestService(t)
					svc.RegisterAdapter(ad)
					game.Adapter = ad.ID()
					require.NoError(t, svc.SaveGame(context.Background(), game))

					files := make(map[string]string, len(members))
					for _, m := range members {
						files[m] = "content of " + m
					}
					archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
					createImportTestZip(t, archivePath, files)

					plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
					require.NoError(t, err)

					result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
					require.NoError(t, err)

					cached, err := svc.GetGameCache(game).ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
					require.NoError(t, err)
					slices.Sort(cached)
					if cached == nil {
						cached = []string{}
					}
					assert.Equal(t, plan.Files, cached,
						"the plan's file list must equal what the ingest actually cached")
					assert.Equal(t, plan.Mod.Name, result.Mod.Name,
						"the plan and the ingest must derive the same mod name")
				})
			}
		})
	}
}

// TestImportArchive_ModNameIsDerivedBeforeTheRewrite pins WHICH derivation
// both halves share: the name is read off the archive's own shape, before
// the adapter moves anything, so an adapter that consults ModName never
// sees a name that depends on its own output.
func TestImportArchive_ModNameIsDerivedBeforeTheRewrite(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	svc.RegisterAdapter(byNameStub{})
	game.Adapter = "by-name"
	require.NoError(t, svc.SaveGame(context.Background(), game))

	archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"named.txt": "x"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)

	// "Flat-2.0", the archive base name - NOT "Flat-2.0.zip", and not
	// "by-name", the wrapper the adapter itself introduces.
	assert.Equal(t, "Flat-2.0", plan.Mod.Name)
	assert.Equal(t, []string{"by-name/Flat-2.0/named.txt"}, plan.Files)

	result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Flat-2.0", result.Mod.Name)

	cached, err := svc.GetGameCache(game).ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	assert.Equal(t, []string{"by-name/Flat-2.0/named.txt"}, cached)
}

// nonCanonicalStub returns destinations spelled non-canonically ("./out/x"),
// which is what an adapter author writes when they build a path by
// concatenation. Legal, and it must reach both halves of the import as the
// same canonical string.
type nonCanonicalStub struct{}

func (nonCanonicalStub) ID() string    { return "noncanonical" }
func (nonCanonicalStub) Label() string { return "Non-canonical" }

func (nonCanonicalStub) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	rewrites := make(map[string]string, len(req.Members))
	for _, m := range req.Members {
		rewrites[m] = "./out/" + filepath.Base(m)
	}
	return adapter.NewLayout("noncanonical", rewrites), nil
}

// TestPlanImportArchive_AgreesWithIngestForANonCanonicalDestination is R1's
// plan-side half: rewritePlannedPaths appended the adapter's RAW destination
// while the ingest wrote the one filepath.Join cleaned, so the plan's file
// list - the confirmation screen, --json and serve's confirm-plan all render
// it - named a path the ingest never created.
func TestPlanImportArchive_AgreesWithIngestForANonCanonicalDestination(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	svc.RegisterAdapter(nonCanonicalStub{})
	game.Adapter = "noncanonical"
	require.NoError(t, svc.SaveGame(context.Background(), game))

	archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.txt": "A"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"out/a.txt"}, plan.Files,
		"the plan must promise the canonical destination, not the adapter's spelling")

	result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.NoError(t, err)

	cached, err := svc.GetGameCache(game).ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	slices.Sort(cached)
	assert.Equal(t, plan.Files, cached,
		"the plan's file list must equal what the ingest actually cached")
}

// memberRecorder keeps every Members slice the seam hands it, so the plan's
// request and the ingest's can be compared as sequences rather than as sets.
// Its Layout is the identity, so it changes nothing about the import.
type memberRecorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (*memberRecorder) ID() string    { return "member-recorder" }
func (*memberRecorder) Label() string { return "Member recorder" }

func (r *memberRecorder) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, slices.Clone(req.Members))
	return adapter.Layout{}, nil
}

// take returns the calls recorded so far and clears the log.
func (r *memberRecorder) take() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

// TestNormalizeRequestMembersAreSortedOnBothSides is R3's regression test:
// the plan's members came from importDeployablePaths, which sorts the flat
// path list, while the ingest's came from filepath.WalkDir, which is
// directory-first. So an archive holding "a.txt" beside "a/b.txt" reached
// the adapter as [a.txt a/b.txt] from the plan and [a/b.txt a.txt] from the
// ingest, and an adapter whose Layout depends on order ("the first .dll is
// the plugin") would lay the two halves out differently - the one invariant
// this seam exists to guarantee.
func TestNormalizeRequestMembersAreSortedOnBothSides(t *testing.T) {
	rec := &memberRecorder{}
	svc, game := newImportArchiveTestService(t)
	svc.RegisterAdapter(rec)
	game.Adapter = rec.ID()
	require.NoError(t, svc.SaveGame(context.Background(), game))

	archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.txt": "A", "a/b.txt": "B"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	planCalls := rec.take()

	_, err = svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.NoError(t, err)
	ingestCalls := rec.take()

	require.NotEmpty(t, planCalls, "the plan must ask the adapter to lay the archive out")
	require.NotEmpty(t, ingestCalls, "and so must the ingest")

	want := []string{"a.txt", "a/b.txt"}
	for _, got := range planCalls {
		assert.Equal(t, want, got, "the plan hands the adapter a sorted member list")
	}
	for _, got := range ingestCalls {
		assert.Equal(t, want, got, "and the ingest hands it the same list in the same order")
	}
}
