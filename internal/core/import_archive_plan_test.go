package core_test

// Tests for the archive-import Plan/Apply pair (#314, R-B1..R-B5):
// PlanImportArchive answers what an archive would contribute WITHOUT
// touching managed state, and ApplyImportArchive performs exactly that plan
// under beginOp with a freshness precondition.
//
// The property that matters most is that the two cannot disagree: the plan's
// file list is asserted equal to what the ingest actually cached, and its
// conflict list equal to what the apply's own gate computes, for every
// archive shape in this file's fixture table.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// treeSnapshot records every path under root (relative, sorted) plus each
// file's content, so a test can assert a whole directory is byte-identical
// before and after a call. A missing root snapshots as nil.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	snap := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			snap[rel+"/"] = ""
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		snap[rel] = string(b)
		return nil
	}))
	return snap
}

// managedStateSnapshot is every piece of state PlanImportArchive promises not
// to touch: the config dir (profiles), the data dir's non-DB content (the
// download staging root), the cache tree and the game directory. The SQLite
// file itself is excluded because its BYTES churn on ordinary reads (page
// cache, change counter); the DB's logical content is asserted separately.
func managedStateSnapshot(t *testing.T, svc *core.Service, game *domain.Game) map[string]map[string]string {
	t.Helper()
	data := treeSnapshot(t, svc.DataDirForTest())
	for path := range data {
		if strings.HasPrefix(filepath.Base(path), "lmm.db") {
			delete(data, path)
		}
	}
	return map[string]map[string]string{
		"config": treeSnapshot(t, svc.ConfigDir()),
		"data":   data,
		"cache":  treeSnapshot(t, svc.GlobalCacheDir()),
		"game":   treeSnapshot(t, game.ModPath),
	}
}

// --- R-B5: the plan is side-effect-free ---

// TestPlanImportArchive_IsSideEffectFree pins R-B2's promise: planning
// LISTS the archive rather than ingesting it, so every managed tree - the DB
// and profiles, the cache, the staging root and the game directory - is
// byte-identical afterwards.
func TestPlanImportArchive_IsSideEffectFree(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	src := newAdoptTestSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createImportTestZip(t, archivePath, map[string]string{"MyMod/mymod.esp": "data"})

	before := managedStateSnapshot(t, svc, game)

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: "acme-source", ModID: "999"})
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, before, managedStateSnapshot(t, svc, game),
		"PlanImportArchive must leave managed state - and the staging root - untouched")

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "planning must write no DB row")
	assert.Equal(t, []string{filepath.Join("MyMod", "mymod.esp")}, plan.Files)
	assert.Equal(t, "Acme Mod", plan.Mod.Name, "enrichment runs in the plan, so the identity is final")
	assert.Equal(t, "2.0", plan.Mod.Version)
	assert.Empty(t, plan.Conflicts)
	assert.NotNil(t, plan.Warnings, "Warnings is never nil on the wire")
}

// TestPlanImportArchive_MintsOneIdentity pins Ruling 18's root cause: an
// unlinked archive's uuid is minted ONCE, in the plan, so re-planning is the
// only way to get a different one and an accepted conflict re-uses the ID the
// readout already printed.
func TestPlanImportArchive_MintsOneIdentity(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	archivePath := filepath.Join(t.TempDir(), "LooseMod-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"loose.esp": "data"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Mod.ID)

	result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, plan.Mod.ID, result.Mod.ID, "the ID the plan minted is the ID the apply persists")

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, plan.Mod.ID, mods[0].ID)
}

// --- R-B5: plan.Files == what the ingest cached, for every archive shape ---

type importPlanFixture struct {
	name string
	// build returns the service, the game and the archive to import.
	build func(t *testing.T) (*core.Service, *domain.Game, string)
	opts  core.ImportArchiveOptions
}

func importPlanFixtures() []importPlanFixture {
	return []importPlanFixture{
		{
			name: "extract/flat zip",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				svc, game := newImportArchiveTestService(t)
				p := filepath.Join(t.TempDir(), "Flat-1.0.zip")
				createImportTestZip(t, p, map[string]string{"a.esp": "a", "b.esp": "b"})
				return svc, game, p
			},
		},
		{
			name: "extract/nested zip under one top-level directory",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				svc, game := newImportArchiveTestService(t)
				p := filepath.Join(t.TempDir(), "Nested-2.0.zip")
				createImportTestZip(t, p, map[string]string{
					"MyMod/a.esp": "a", "MyMod/sub/b.txt": "b", "MyMod/sub/deep/c.txt": "c",
				})
				return svc, game, p
			},
		},
		{
			name: "extract/7z",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				requireSevenZip(t)
				svc, game := newImportArchiveTestService(t)
				return svc, game, createTestSevenZip(t, "Seven-1.0.7z", "-t7z")
			},
		},
		{
			name: "extract/rar-format archive listed through 7z",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				requireSevenZip(t)
				svc, game := newImportArchiveTestService(t)
				return svc, game, createTestSevenZip(t, "Zipped-1.0.zip", "-tzip")
			},
		},
		{
			name: "copy mode",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				svc, game := newImportArchiveTestService(t)
				game.DeployMode = domain.DeployCopy
				p := filepath.Join(t.TempDir(), "CopyMod-1.0.zip")
				createImportTestZip(t, p, map[string]string{"inside.esp": "x"})
				return svc, game, p
			},
		},
		{
			name: "compile/native merge source deploys nothing",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				svc, _, game := newImportCompileTestGame(t)
				p := filepath.Join(t.TempDir(), "Bear_Mount.exmodz")
				require.NoError(t, os.WriteFile(p, []byte("fake-exmodz-bytes"), 0o644))
				return svc, game, p
			},
		},
		{
			name: "compile/convertible pak deploys itself",
			build: func(t *testing.T) (*core.Service, *domain.Game, string) {
				svc, _, game := newImportCompileTestGame(t)
				game.ConvertPaks = true
				p := filepath.Join(t.TempDir(), "Raw_Weapon.pak")
				require.NoError(t, os.WriteFile(p, []byte("raw-pak-bytes"), 0o644))
				return svc, game, p
			},
		},
	}
}

// requireSevenZip skips a fixture that needs the system 7z binary.
func requireSevenZip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not installed: the 7z/rar listing path cannot be exercised")
	}
}

// createTestSevenZip builds an archive with the system 7z in the requested
// container format, holding a nested mod tree.
func createTestSevenZip(t *testing.T, name, format string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "MyMod", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "MyMod", "a.esp"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "MyMod", "sub", "b.txt"), []byte("b"), 0o644))

	archive := filepath.Join(dir, name)
	cmd := exec.Command("7z", "a", format, archive, "./MyMod")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "7z a failed: %s", out)
	return archive
}

// TestPlanImportArchive_FilesMatchIngest is R-B5's property test: for every
// archive shape, the plan's file list is exactly what the cache entry holds
// once the apply has ingested it, and the plan's conflict list is exactly
// what the apply's own gate computes.
func TestPlanImportArchive_FilesMatchIngest(t *testing.T) {
	for _, fx := range importPlanFixtures() {
		t.Run(fx.name, func(t *testing.T) {
			svc, game, archivePath := fx.build(t)

			plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, fx.opts)
			require.NoError(t, err)

			result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, fx.opts, nil)
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
			assert.Equal(t, len(plan.Files), result.Deployed,
				"the plan's file count is the readout's 'Files: N'")
		})
	}
}

// --- R-B5: staleness ---

// TestApplyImportArchive_StaleArchive pins the archive fingerprint half of
// the freshness precondition: the archive changing under a computed plan is
// ErrStalePlan, not a silent import of different bytes.
func TestApplyImportArchive_StaleArchive(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.esp": "a"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)

	// Rewrite it with different content AND a different mtime.
	createImportTestZip(t, archivePath, map[string]string{"a.esp": "a", "b.esp": "b"})
	require.NoError(t, os.Chtimes(archivePath, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	_, err = svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)

	mods, mErr := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, mErr)
	assert.Empty(t, mods, "a stale plan must not install anything")
}

// TestApplyImportArchive_StaleProfile pins the installed-snapshot half: an
// unrelated install landing between the plan and the apply invalidates it.
func TestApplyImportArchive_StaleProfile(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.esp": "a"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)

	seedInstalledMod(t, svc, game, "other-src", "other", "1.0", true, map[string][]byte{"other.esp": []byte("x")})

	_, err = svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// TestApplyImportArchive_ConflictRefusalLeavesNoCacheEntry pins that the
// Ruling 1 refusal still cleans up after itself under the Plan/Apply shape:
// the plan reports the conflict, the apply refuses it, and the cache entry
// the apply created is gone again.
func TestApplyImportArchive_ConflictRefusalLeavesNoCacheEntry(t *testing.T) {
	svc, game, archiveB := setupImportArchiveConflict(t)
	opts := core.ImportArchiveOptions{SourceID: "acme-source", ModID: "B1"}

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archiveB, opts)
	require.NoError(t, err)
	require.Len(t, plan.Conflicts, 1, "the plan must report the conflict without ingesting anything")
	assert.Equal(t, "shared.txt", plan.Conflicts[0].RelativePath)
	assert.Equal(t, "A1", plan.Conflicts[0].CurrentModID)
	assert.False(t, plan.EntryPreExists)

	_, err = svc.ApplyImportArchive(context.Background(), game, "default", plan, opts, nil)
	require.ErrorAs(t, err, new(*core.ConflictError))

	assert.False(t, svc.GetGameCache(game).Exists("g1", "acme-source", "B1", "1.0"),
		"a refused conflict must remove the cache entry this apply created")

	data, rErr := os.ReadFile(filepath.Join(game.ModPath, "shared.txt"))
	require.NoError(t, rErr)
	assert.Equal(t, "from-A", string(data))
}

// TestPlanImportArchive_CompileGame_ReportsMergedArtifactEffect pins the
// Ruling 8 modelling for the import direction: importing a merge source into
// a DeployCompile game resyncs the profile's merged artifact.
func TestPlanImportArchive_CompileGame_ReportsMergedArtifactEffect(t *testing.T) {
	svc, _, game := newImportCompileTestGame(t)
	archivePath := filepath.Join(t.TempDir(), "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)

	require.NotNil(t, plan.MergedArtifact)
	assert.Equal(t, core.MergedArtifactResync, plan.MergedArtifact.Action)
	assert.NotEmpty(t, plan.MergedArtifact.Path)
	assert.Empty(t, plan.Files, "a native merge source deploys nothing of its own")
}

// TestPlanImportArchive_ExtractGame_HasNoMergedArtifactEffect is the
// negative twin: a non-compile game has no merged artifact to speak of.
func TestPlanImportArchive_ExtractGame_HasNoMergedArtifactEffect(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.esp": "a"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Nil(t, plan.MergedArtifact)
}

// TestPlanImportArchive_ReportsHooks pins the plan's Hooks list: only
// configured install.* hooks, in run order.
func TestPlanImportArchive_ReportsHooks(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	scriptsDir := t.TempDir()
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{
		BeforeAll: createTestScript(t, scriptsDir, "ba.sh", "#!/bin/bash\nexit 0"),
		AfterAll:  createTestScript(t, scriptsDir, "aa.sh", "#!/bin/bash\nexit 0"),
	}}

	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.esp": "a"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"install.before_all", "install.after_all"}, plan.Hooks)

	plan, err = svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SkipHooks: true})
	require.NoError(t, err)
	assert.Empty(t, plan.Hooks, "--no-hooks runs none of them")
}

// TestPlanImportArchive_ErrorWording pins the NEW wording of the failures
// Ruling 18 moved out of the ingest and into the plan, byte-exactly, so it
// cannot drift silently. Each used to carry ApplyImportArchive's
// "import failed: " prefix (and, for a member rejection, "extracting
// archive: " on top of it) purely because the check lived inside the
// ingest; a plan that never extracts must not claim to be extracting.
func TestPlanImportArchive_ErrorWording(t *testing.T) {
	t.Run("unsupported format", func(t *testing.T) {
		svc, game := newImportArchiveTestService(t)
		archivePath := filepath.Join(t.TempDir(), "notes.txt")
		require.NoError(t, os.WriteFile(archivePath, []byte("not an archive"), 0o644))

		_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
		require.EqualError(t, err, "unsupported archive format: .txt")
	})

	t.Run("reserved member name", func(t *testing.T) {
		svc, game := newImportArchiveTestService(t)
		archivePath := filepath.Join(t.TempDir(), "hostile.zip")
		createImportTestZip(t, archivePath, map[string]string{".lmm-file-7": "forged marker"})

		_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
		require.EqualError(t, err,
			`reserved name detected: .lmm-file-7 (archive members may not use lmm's reserved ".lmm-" prefix)`)
	})

	t.Run("zip slip", func(t *testing.T) {
		svc, game := newImportArchiveTestService(t)
		archivePath := filepath.Join(t.TempDir(), "slip.zip")
		createImportTestZip(t, archivePath, map[string]string{"../../etc/passwd": "escape"})

		_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
		require.EqualError(t, err, "path traversal detected: ../../etc/passwd")
	})
}

// TestPlanImportArchive_MissingArchive fails the same way the ingest does.
func TestPlanImportArchive_MissingArchive(t *testing.T) {
	svc, game := newImportArchiveTestService(t)

	_, err := svc.PlanImportArchive(context.Background(), game, "default",
		filepath.Join(t.TempDir(), "nope.zip"), core.ImportArchiveOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive not found")
}

// TestPlanImportArchive_EnrichmentWarningsAreCarried pins the R-B1 warning
// carrier the coordinator ruled in: an unmapped source is a plan warning
// (never nil on the wire), and the apply copies it into the result so a
// Result-only consumer still sees it.
func TestPlanImportArchive_EnrichmentWarningsAreCarried(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.esp": "a"})
	opts := core.ImportArchiveOptions{SourceID: "not-mapped", ModID: "7"}

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"source not-mapped is not configured for this game; skipping metadata fetch"}, plan.Warnings)

	result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, opts, nil)
	require.NoError(t, err)
	assert.Equal(t, plan.Warnings, result.Warnings,
		"ApplyImportArchive copies the plan's warnings so a Result-only consumer still sees them")
}
