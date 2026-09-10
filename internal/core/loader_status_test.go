package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGameDir builds a game installation carrying the on-disk markers that
// answer "Mono or IL2CPP" and "native Linux build or Proton" - the fixture
// per runtime/bootstrap combination #359 asks for.
//
// The markers are the ones the spike named (§4) plus the two that identify a
// Windows build: a native Unity Linux game ships UnityPlayer.so and a
// <Game>.x86_64 launcher, a Windows one ships UnityPlayer.dll and
// <Game>.exe. Nothing here launches anything or reads a Steam config.
func fakeGameDir(t *testing.T, runtime domain.LoaderRuntime, bootstrap domain.LoaderBootstrap) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	switch runtime {
	case domain.LoaderRuntimeIL2CPP:
		write("Game_Data/il2cpp_data/Metadata/global-metadata.dat")
	case domain.LoaderRuntimeMono:
		write("Game_Data/Managed/Assembly-CSharp.dll")
	}
	switch bootstrap {
	case domain.LoaderBootstrapNative:
		write("UnityPlayer.so")
		write("Game.x86_64")
	case domain.LoaderBootstrapProton:
		write("UnityPlayer.dll")
		write("Game.exe")
	}
	return root
}

// TestDetectLoaderTarget covers every runtime/bootstrap combination, plus the
// honest answer for a directory with no markers at all: unknown, not a guess.
func TestDetectLoaderTarget(t *testing.T) {
	for _, runtime := range []domain.LoaderRuntime{domain.LoaderRuntimeMono, domain.LoaderRuntimeIL2CPP} {
		for _, bootstrap := range []domain.LoaderBootstrap{domain.LoaderBootstrapNative, domain.LoaderBootstrapProton} {
			t.Run(runtime.String()+"/"+bootstrap.String(), func(t *testing.T) {
				gotRuntime, gotBootstrap := core.DetectLoaderTarget(fakeGameDir(t, runtime, bootstrap))
				assert.Equal(t, runtime, gotRuntime)
				assert.Equal(t, bootstrap, gotBootstrap)
			})
		}
	}

	// Review F13: with markers for both, the answer used to be whichever
	// name os.ReadDir returned first - lexical sort deciding which BepInEx
	// build and which launch option a user is told to use. It is decided by
	// EVIDENCE now: UnityPlayer.so/.dll are Unity's own runtime and beat the
	// generic extension heuristics, and native wins a genuine tie because a
	// depot shipping both is one whose Linux build is what actually runs.
	t.Run("the specific markers beat the generic extension heuristics", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			files []string
			want  domain.LoaderBootstrap
		}{
			{"UnityPlayer.so beside a .exe", []string{"UnityPlayer.so", "Game.exe"}, domain.LoaderBootstrapNative},
			{"UnityPlayer.dll beside a .x86_64", []string{"UnityPlayer.dll", "Game.x86_64"}, domain.LoaderBootstrapProton},
			{"both runtimes: native wins", []string{"UnityPlayer.so", "UnityPlayer.dll"}, domain.LoaderBootstrapNative},
			{"both launchers: native wins", []string{"Game.x86_64", "Game.exe"}, domain.LoaderBootstrapNative},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := t.TempDir()
				for _, f := range tc.files {
					require.NoError(t, os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644))
				}
				_, gotBootstrap := core.DetectLoaderTarget(root)
				assert.Equal(t, tc.want, gotBootstrap)
			})
		}
	})

	t.Run("no markers is unknown, never a guess", func(t *testing.T) {
		gotRuntime, gotBootstrap := core.DetectLoaderTarget(t.TempDir())
		assert.Equal(t, domain.LoaderRuntimeUnknown, gotRuntime)
		assert.Equal(t, domain.LoaderBootstrapUnknown, gotBootstrap)
	})

	t.Run("a missing directory is unknown, not an error", func(t *testing.T) {
		gotRuntime, gotBootstrap := core.DetectLoaderTarget(filepath.Join(t.TempDir(), "nope"))
		assert.Equal(t, domain.LoaderRuntimeUnknown, gotRuntime)
		assert.Equal(t, domain.LoaderBootstrapUnknown, gotBootstrap)
	})
}

// TestBepInExLaunchOption is the string a user pastes into Steam. Getting it
// wrong is the difference between a modded game and a vanilla one that gives
// no sign anything is missing, so both are pinned exactly (spike §1.2).
func TestBepInExLaunchOption(t *testing.T) {
	assert.Equal(t, "./run_bepinex.sh %command%", core.BepInExLaunchOption(domain.LoaderBootstrapNative))
	assert.Equal(t, `WINEDLLOVERRIDES="winhttp=n,b" %command%`, core.BepInExLaunchOption(domain.LoaderBootstrapProton))
	assert.Empty(t, core.BepInExLaunchOption(domain.LoaderBootstrapUnknown),
		"an unanswered bootstrap has no launch option to print - lmm does not pick one")
}

// TestLoaderStatus_ReportsWhatIsThereAndWhatToPaste is the document `lmm game
// show` and the web game page render: the declaration, what the disk says,
// the launch option for that bootstrap, and whether the preloader is actually
// installed.
func TestLoaderStatus_ReportsWhatIsThereAndWhatToPaste(t *testing.T) {
	svc := newFlowsTestService(t)
	install := fakeGameDir(t, domain.LoaderRuntimeMono, domain.LoaderBootstrapProton)
	require.NoError(t, os.MkdirAll(filepath.Join(install, "BepInEx", "core"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "BepInEx", "core", "BepInEx.Preloader.dll"), []byte("x"), 0o644))

	game := &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: install, ModPath: install,
		Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	status, err := svc.LoaderStatus(context.Background(), "valheim")
	require.NoError(t, err)
	require.NotNil(t, status.Declared)
	assert.Equal(t, domain.LoaderKindBepInEx, status.Declared.Kind)

	// The declaration answered neither question, so the report carries what
	// the disk says - and the launch option for THAT bootstrap.
	assert.Equal(t, domain.LoaderRuntimeMono, status.DetectedRuntime)
	assert.Equal(t, domain.LoaderBootstrapProton, status.DetectedBootstrap)
	assert.Equal(t, domain.LoaderBootstrapProton, status.EffectiveBootstrap)
	assert.Equal(t, `WINEDLLOVERRIDES="winhttp=n,b" %command%`, status.LaunchOption)
	assert.True(t, status.Installed, "the preloader is present")
}

// A DECLARED bootstrap wins over the detected one: the user has seen their
// own installation and lmm's markers are inference. The report still carries
// both, so a disagreement is visible rather than silently resolved.
func TestLoaderStatus_DeclarationWinsOverDetection(t *testing.T) {
	svc := newFlowsTestService(t)
	install := fakeGameDir(t, domain.LoaderRuntimeMono, domain.LoaderBootstrapProton)
	game := &domain.Game{
		ID: "g", Name: "G", InstallPath: install, ModPath: install,
		Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	status, err := svc.LoaderStatus(context.Background(), "g")
	require.NoError(t, err)
	assert.Equal(t, domain.LoaderBootstrapNative, status.EffectiveBootstrap)
	assert.Equal(t, "./run_bepinex.sh %command%", status.LaunchOption)
	assert.Equal(t, domain.LoaderBootstrapProton, status.DetectedBootstrap)
	require.NotEmpty(t, status.Warnings, "a declaration that contradicts the disk is worth saying out loud")
}

// A game with NO loader block still gets a report: it is what tells a user
// which build they would need and which launch option would apply, which is
// the whole point of printing guidance rather than automating the setup.
func TestLoaderStatus_UndeclaredGameStillReportsWhatItWouldNeed(t *testing.T) {
	svc := newFlowsTestService(t)
	install := fakeGameDir(t, domain.LoaderRuntimeIL2CPP, domain.LoaderBootstrapNative)
	game := &domain.Game{ID: "g", Name: "G", InstallPath: install, ModPath: install}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	status, err := svc.LoaderStatus(context.Background(), "g")
	require.NoError(t, err)
	assert.Nil(t, status.Declared)
	assert.False(t, status.Installed)
	assert.Equal(t, domain.LoaderRuntimeIL2CPP, status.DetectedRuntime)
	assert.Equal(t, "./run_bepinex.sh %command%", status.LaunchOption)
}

// An unknown game is domain.ErrGameNotFound, the 404 every other
// game-scoped surface answers with.
func TestLoaderStatus_UnknownGame(t *testing.T) {
	svc := newFlowsTestService(t)
	_, err := svc.LoaderStatus(context.Background(), "nope")
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

// TestLoaderStatus_AGameWithNothingLoaderShapedIsNotRelevant is review F7's
// core half: the "set it with --loader-bootstrap" warning was appended
// unconditionally, so `lmm game show icarus` - an Unreal game that neither
// has nor needs a mod loader - was told to configure one. The warning is
// only honest for a game something says is loader-relevant.
func TestLoaderStatus_AGameWithNothingLoaderShapedIsNotRelevant(t *testing.T) {
	svc := newFlowsTestService(t)
	install := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Content", "Paks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "Content", "Paks", "game.pak"), []byte("pak"), 0o644))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: install, ModPath: install,
	}))

	status, err := svc.LoaderStatus(context.Background(), "icarus")
	require.NoError(t, err)
	assert.False(t, status.Relevant(), "nothing declares or looks like a loader game")
	assert.Empty(t, status.Warnings, "so there is no loader advice to give: %v", status.Warnings)
}

// The declaration alone makes a game relevant, whatever the disk says - and
// there the unanswered bootstrap IS worth saying out loud, because it is the
// difference between a launch option lmm can hand over and one it cannot.
func TestLoaderStatus_ADeclaredGameStillWarnsAboutAnUnansweredBootstrap(t *testing.T) {
	svc := newFlowsTestService(t)
	install := t.TempDir()
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "declared", Name: "Declared", InstallPath: install, ModPath: install,
		Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx},
	}))

	status, err := svc.LoaderStatus(context.Background(), "declared")
	require.NoError(t, err)
	assert.True(t, status.Relevant())
	require.NotEmpty(t, status.Warnings)
	assert.Contains(t, status.Warnings[0], "--loader-bootstrap")
}

// TestLoaderStatus_AnInstalledButUndeclaredLoaderIsRelevant is re-review R3:
// Relevant() consulted only the declaration and the two DETECTED enums, so a
// game with BepInEx actually installed - and a LogOutput.log proving it has
// run - but no declaration and no Unity/launcher marker was told nothing at
// all. That is the user the report exists for: undeclared, mid-setup, one
// `lmm game edit --loader bepinex` from working.
func TestLoaderStatus_AnInstalledButUndeclaredLoaderIsRelevant(t *testing.T) {
	svc := newFlowsTestService(t)
	install := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(install, "BepInEx", "core"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "BepInEx", "core", "BepInEx.Preloader.dll"), []byte("pe"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(install, "BepInEx", "LogOutput.log"), []byte("log"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(install, "start.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "mid-setup", Name: "Mid Setup", InstallPath: install, ModPath: install,
	}))

	status, err := svc.LoaderStatus(context.Background(), "mid-setup")
	require.NoError(t, err)
	require.True(t, status.Installed, "the fixture must be the installed-but-undeclared state")
	assert.Equal(t, domain.LoaderBootstrapUnknown, status.DetectedBootstrap, "and nothing on disk answers the bootstrap")
	assert.True(t, status.Relevant(), "BepInEx being ON DISK is as loader-relevant as a declaration")
	require.NotEmpty(t, status.Warnings, "so the bootstrap question is worth asking")
	assert.Contains(t, status.Warnings[0], "--loader-bootstrap")
}
