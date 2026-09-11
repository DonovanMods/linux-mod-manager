package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAdapterTestService is newPlanTestService with an adapter registry the
// test can add to. Everything else about the Service is the default, which
// is the point: the registry a Service builds for itself already holds the
// generic identity.
func newAdapterTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// compileStub is the smallest adapter that satisfies adapter.MergeCompiler,
// so the migration and the deploy_mode composition rule can be tested
// without internal/adapter/icarus, which does not exist until U2.
type compileStub struct {
	id string
}

func (c compileStub) ID() string    { return c.id }
func (c compileStub) Label() string { return c.id }
func (c compileStub) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}
func (compileStub) ValidateSource(string) error { return nil }
func (compileStub) MergeCompile(context.Context, string, []adapter.MergeSource, string) ([]string, []adapter.MergeFailure, error) {
	return nil, nil, nil
}
func (compileStub) ResolveBaseArtifact(*domain.Game) (string, error) { return "", nil }
func (compileStub) FingerprintBase(string) (string, error)           { return "", nil }
func (compileStub) IsNativeMergeSource(string) bool                  { return false }
func (compileStub) IsConvertibleArtifact(string) bool                { return false }
func (compileStub) ClassifyMergeSource(string) (string, bool)        { return "", false }
func (compileStub) MergedArtifactName() string                       { return "merged.pak" }
func (compileStub) MergedArtifactLabel() string                      { return "Merged" }
func (compileStub) RestoredArtifactName(modID string) string         { return modID + "_P.pak" }

// routingStub is an adapter with a FileRouter and a Verifier, for the two
// seams whose identity branch is otherwise indistinguishable from "not
// wired at all".
type routingStub struct {
	compileStub
	copyOnce map[string]bool
	skip     map[string]bool
	findings []adapter.Finding
	precond  error
}

func (r routingStub) RouteFile(_ *domain.Game, rel string) adapter.FileRoute {
	switch {
	case r.copyOnce[rel]:
		return adapter.RouteCopyOnce
	case r.skip[rel]:
		return adapter.RouteSkip
	}
	return adapter.RouteLink
}

func (r routingStub) Verify(context.Context, adapter.VerifyRequest) ([]adapter.Finding, error) {
	return r.findings, nil
}

func (r routingStub) CheckPreconditions(*domain.Game, []domain.InstalledMod) error {
	return r.precond
}

func TestAdapterResolution(t *testing.T) {
	svc := newAdapterTestService(t)

	t.Run("a game with no adapter key resolves to generic-files", func(t *testing.T) {
		a, err := svc.AdapterFor(&domain.Game{ID: "skyrim-se"})
		require.NoError(t, err)
		assert.Equal(t, adapter.GenericID, a.ID())
		assert.Equal(t, adapter.GenericID, svc.AdapterName(&domain.Game{ID: "skyrim-se"}))
	})

	t.Run("an explicit adapter wins", func(t *testing.T) {
		svc.RegisterAdapter(compileStub{id: "icarus"})
		assert.Equal(t, "icarus", svc.AdapterName(&domain.Game{ID: "g", Adapter: "icarus"}))
	})

	t.Run("an unregistered adapter fails loud, naming the game and the registered set", func(t *testing.T) {
		_, err := svc.AdapterFor(&domain.Game{ID: "someday", Adapter: "melonloader"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `game "someday"`)
		assert.Contains(t, err.Error(), `unknown adapter "melonloader"`)
		assert.Contains(t, err.Error(), adapter.GenericID)
	})

	t.Run("ListAdapters serves the frontend the registered names", func(t *testing.T) {
		assert.Contains(t, svc.ListAdapters(), adapter.GenericID)
	})
}

func TestDeployModeCompileMigration(t *testing.T) {
	t.Run("with no icarus adapter registered the compile game stays on the identity", func(t *testing.T) {
		// U1's state exactly: the derivation is inert, so the compile path
		// keeps resolving its MergeCompiler from the game's sources and
		// every Icarus golden holds.
		svc := newAdapterTestService(t)
		game := &domain.Game{ID: "icarus", DeployMode: domain.DeployCompile}

		assert.Equal(t, adapter.GenericID, svc.AdapterName(game))
		a, err := svc.AdapterFor(game)
		require.NoError(t, err)
		_, compiles := adapter.Compiler(a)
		assert.False(t, compiles)
	})

	t.Run("once an icarus adapter is registered the derivation goes live", func(t *testing.T) {
		// U2's state: no code changes between the two subtests - only the
		// registration.
		svc := newAdapterTestService(t)
		svc.RegisterAdapter(compileStub{id: "icarus"})
		game := &domain.Game{ID: "icarus", DeployMode: domain.DeployCompile}

		assert.Equal(t, "icarus", svc.AdapterName(game))
		a, err := svc.AdapterFor(game)
		require.NoError(t, err)
		_, compiles := adapter.Compiler(a)
		assert.True(t, compiles, "the derived adapter must be the compile capability")
	})

	t.Run("an explicit adapter still wins over the derivation", func(t *testing.T) {
		svc := newAdapterTestService(t)
		svc.RegisterAdapter(compileStub{id: "icarus"})
		svc.RegisterAdapter(compileStub{id: "other"})
		assert.Equal(t, "other", svc.AdapterName(&domain.Game{
			ID: "g", Adapter: "other", DeployMode: domain.DeployCompile,
		}))
	})

	t.Run("an explicit non-compiling adapter with deploy_mode compile is refused by name", func(t *testing.T) {
		svc := newAdapterTestService(t)
		_, err := svc.AdapterFor(&domain.Game{
			ID: "muddled", Adapter: adapter.GenericID, DeployMode: domain.DeployCompile,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deploy_mode: compile")
		assert.Contains(t, err.Error(), adapter.GenericID)
	})

	t.Run("a compile game with NO adapter key is accepted", func(t *testing.T) {
		// The rule above fires for an EXPLICIT adapter only: an untouched
		// pre-#353 games.yaml must keep working with no user action, which
		// is the whole point of keeping deploy_mode: compile for 2.0 (OQ1).
		svc := newAdapterTestService(t)
		_, err := svc.AdapterFor(&domain.Game{ID: "icarus", DeployMode: domain.DeployCompile})
		require.NoError(t, err)
	})
}

func TestGenericSeamsTakeTheIdentityBranch(t *testing.T) {
	svc := newAdapterTestService(t)
	game := &domain.Game{ID: "skyrim-se", InstallPath: t.TempDir()}
	game.ModPath = game.InstallPath

	t.Run("the archive layout rewrites nothing", func(t *testing.T) {
		members := []string{"Data/mod.esp", "Data/meshes/x.nif"}
		layout, err := svc.archiveLayout(game, "MyMod", members)
		require.NoError(t, err)
		assert.False(t, layout.Applies())
		planned, err := rewritePlannedPaths(layout, members)
		require.NoError(t, err)
		assert.Equal(t, members, planned)
	})

	t.Run("the tree rewriter is a no-op on disk", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "Data"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "Data", "mod.esp"), []byte("x"), 0o644))

		members := []string{"Data/mod.esp"}
		layout, err := svc.archiveLayout(game, "MyMod", members)
		require.NoError(t, err)
		got, err := rewriteExtractedTree(root, layout, members)
		require.NoError(t, err)
		assert.Equal(t, members, got)
		assert.FileExists(t, filepath.Join(root, "Data", "mod.esp"))
	})

	t.Run("every file routes to the linker", func(t *testing.T) {
		a, err := svc.AdapterFor(game)
		require.NoError(t, err)
		files := []string{"Data/mod.esp", "BepInEx/config/plugin.cfg"}
		assert.Equal(t, files, routeDeployables(a, game, files))
		assert.Empty(t, adapterCopyOnceFiles(a, game, files))
	})

	t.Run("no precondition and no verify finding", func(t *testing.T) {
		require.NoError(t, svc.checkAdapterPreconditions("skyrim-se", nil))
		a, err := svc.AdapterFor(game)
		require.NoError(t, err)
		findings, err := adapter.Verify(context.Background(), a, adapter.VerifyRequest{Game: game})
		require.NoError(t, err)
		assert.Empty(t, findings)
	})
}

func TestRouteFileSeamNarrowsDeployables(t *testing.T) {
	// The identity branch above is indistinguishable from "not wired", so
	// this is the proof the route is actually consulted.
	game := &domain.Game{ID: "g"}
	a := routingStub{
		compileStub: compileStub{id: "router"},
		copyOnce:    map[string]bool{"BepInEx/config/plugin.cfg": true},
		skip:        map[string]bool{"README.md": true},
	}
	files := []string{"BepInEx/config/plugin.cfg", "BepInEx/plugins/mod.dll", "README.md"}

	assert.Equal(t, []string{"BepInEx/plugins/mod.dll"}, routeDeployables(a, game, files))
	assert.Equal(t, []string{"BepInEx/config/plugin.cfg"}, adapterCopyOnceFiles(a, game, files))
}

func TestAdapterPreconditionErrorShape(t *testing.T) {
	svc := newAdapterTestService(t)
	svc.RegisterAdapter(routingStub{
		compileStub: compileStub{id: "refuser"},
		precond:     errors.New("install BepInEx first"),
	})
	svc.games["lc"] = &domain.Game{ID: "lc", Adapter: "refuser", DeployMode: domain.DeployCompile}

	err := svc.checkAdapterPreconditions("lc", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, adapter.ErrPreconditionUnmet)

	var typed *AdapterPreconditionError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "lc", typed.GameID)
	assert.Equal(t, "refuser", typed.Adapter)
	assert.Contains(t, typed.Reason, "install BepInEx first")

	body, merr := json.Marshal(typed.Details())
	require.NoError(t, merr)
	assert.JSONEq(t, `{"game_id":"lc","adapter":"refuser","reason":"install BepInEx first"}`, string(body))
}

func TestCopyOnceWritesOnceAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.cfg")
	dst := filepath.Join(dir, "nested", "dst.cfg")
	require.NoError(t, os.WriteFile(src, []byte("shipped"), 0o644))

	require.NoError(t, copyOnce(src, dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "shipped", string(got))

	// The user hand-edits it; a second deploy must leave the edit alone.
	require.NoError(t, os.WriteFile(dst, []byte("edited"), 0o644))
	require.NoError(t, copyOnce(src, dst))
	got, err = os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "edited", string(got))
}

func TestRewriteExtractedTreeAppliesALayout(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "plugins"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plugins", "mod.dll"), []byte("dll"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("doc"), 0o644))

	layout := adapter.NewLayout("game-root-relative", map[string]string{
		"plugins/mod.dll": "BepInEx/plugins/mod.dll",
		"README.md":       "",
	})
	got, err := rewriteExtractedTree(root, layout, []string{"plugins/mod.dll", "README.md"})
	require.NoError(t, err)

	assert.Equal(t, []string{"BepInEx/plugins/mod.dll"}, got)
	assert.FileExists(t, filepath.Join(root, "BepInEx", "plugins", "mod.dll"))
	assert.NoFileExists(t, filepath.Join(root, "README.md"))
	assert.NoDirExists(t, filepath.Join(root, "plugins"), "an emptied source directory must not survive")
}

func TestRewriteExtractedTreeRefusesAnEscapingPath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.dll"), []byte("x"), 0o644))

	layout := adapter.NewLayout("hostile", map[string]string{"a.dll": "../escaped.dll"})
	_, err := rewriteExtractedTree(root, layout, []string{"a.dll"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escaping")
}

// TestValidateInstallFileSelectionAsksTheAdapter is I5's regression test
// (#411), and U2 (#412) is the pass it was written for. Every other
// compile-capability site asked adapterCompiler first;
// ValidateInstallFileSelection kept a direct src.(MergeCompiler) assertion
// that was neither marked nor inventoried. U2 moved the MergeCompiler
// methods off the Icarus SOURCE, so that assertion would now be false for
// every Icarus selection and #211's guard against mixing the .exmodz
// variant with any other file in one selection would have become dead code
// returning nil - a released fix regressing in a deletion pass, with
// nothing else failing. Keep this test.
// exmodzCompiler is compileStub with the one format question the
// variant-exclusivity rule asks.
type exmodzCompiler struct{ compileStub }

func (exmodzCompiler) IsNativeMergeSource(name string) bool {
	return strings.HasSuffix(name, ".exmodz")
}

// plainTestSource is the smallest source.ModSource that implements NO
// optional capability - in particular no adapter.MergeCompiler.
type plainTestSource struct{}

func (plainTestSource) ID() string      { return "plain" }
func (plainTestSource) Name() string    { return "Plain" }
func (plainTestSource) AuthURL() string { return "" }
func (plainTestSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (plainTestSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (plainTestSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, domain.ErrModNotFound
}
func (plainTestSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (plainTestSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (plainTestSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (plainTestSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

func TestValidateInstallFileSelectionAsksTheAdapter(t *testing.T) {
	svc := newAdapterTestService(t)
	svc.RegisterAdapter(exmodzCompiler{compileStub: compileStub{id: "compiler"}})
	game := &domain.Game{ID: "g1", Adapter: "compiler", SourceIDs: map[string]string{"plain": "g1"}}
	svc.games[game.ID] = game
	svc.RegisterSource(plainTestSource{})

	files := []domain.DownloadableFile{
		{ID: "pak", FileName: "Mod_P.pak"},
		{ID: "exmodz", FileName: "Mod.exmodz"},
	}

	// No source anywhere implements MergeCompiler; the game's ADAPTER does,
	// and since U2 that is the only thing asked.
	err := svc.ValidateInstallFileSelection(game, files)
	require.Error(t, err, "the variant-exclusivity rule must follow the adapter, not only the source")
	assert.Contains(t, err.Error(), "alternate forms of the same mod")
}

// TestBareServiceRegistryIsPerService is M6's regression test: a Service
// built as a bare &Service{} literal - which internal white-box tests do -
// used to read AND WRITE a package-level default registry, so one test's
// RegisterAdapter leaked into every other bare Service in the package.
func TestBareServiceRegistryIsPerService(t *testing.T) {
	one, two := &Service{}, &Service{}

	one.RegisterAdapter(compileStub{id: "leaky"})

	assert.Contains(t, one.ListAdapters(), "leaky")
	assert.NotContains(t, two.ListAdapters(), "leaky",
		"a registration on one Service must not reach another")
	assert.Contains(t, two.ListAdapters(), adapter.GenericID,
		"but every Service still resolves the built-in identity")
}
