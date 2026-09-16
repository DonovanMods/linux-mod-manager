package core_test

// Design decision 11's warning (#353, #413 review F5): a game that HAS
// BepInEx - declared, or installed where lmm can see it - but resolves to a
// different adapter.
//
// Before U3, core's BepInEx normaliser ran for any game declaring the
// loader, whatever its `adapter:` said. U3 made the adapter the only thing
// that lays an archive out, which is the design, but left out the warning
// the design pairs with it - so an explicit `adapter: generic-files`, or a
// `deploy_mode: compile` that selects icarus, on a loader game now deployed
// a Thunderstore package's manifest.json and icon.png into the Steam
// install directory and linked its BepInEx/config files from the shared
// cache, with nothing said anywhere. These tests reproduce that path and
// pin that it is no longer silent.

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/icarus"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// thunderstorePackage is the reproduced archive: shape A, with the metadata
// every Thunderstore package carries at its root and a seeded config.
var thunderstorePackage = map[string]string{
	"BepInEx/plugins/Skinwalkers/SkinwalkerMod.dll": "assembly",
	"BepInEx/config/Skinwalkers.cfg":                "[General]\n",
	"manifest.json":                                 `{"name":"Skinwalkers"}`,
	"icon.png":                                      "png",
}

// bypassCase is one way a loader game ends up on another adapter.
type bypassCase struct {
	adapterID string // what the game resolves to instead of bepinex
	setup     func(t *testing.T, svc *core.Service, game *domain.Game)
	// wants are fragments every warning about this game must carry: which
	// adapter, and the fix that applies to THIS configuration.
	wants []string
}

var bypassCases = map[string]bypassCase{
	"a declared loader under an explicit generic-files adapter": {
		adapterID: "generic-files",
		setup: func(t *testing.T, _ *core.Service, game *domain.Game) {
			game.Adapter = "generic-files"
			game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
		},
		wants: []string{
			`"generic-files"`,
			"lmm game edit lethal-company --adapter bepinex",
			"remove the `loader:` block",
		},
	},
	"a declared loader on a deploy_mode: compile game": {
		adapterID: "icarus",
		setup: func(t *testing.T, svc *core.Service, game *domain.Game) {
			svc.RegisterAdapter(icarus.New())
			game.DeployMode = domain.DeployCompile
			game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
		},
		wants: []string{
			`"icarus"`,
			"deploy_mode: compile",
			"remove the `loader:` block",
		},
	},
	// #413 review F6: the loader lmm can SEE counts as well as the one the
	// game declares, exactly as it does when the adapter is resolved.
	"an installed but undeclared loader under an explicit generic-files adapter": {
		adapterID: "generic-files",
		setup: func(t *testing.T, _ *core.Service, game *domain.Game) {
			game.Adapter = "generic-files"
			bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapUnknown, time.Time{})
		},
		wants: []string{
			`"generic-files"`,
			"lmm game edit lethal-company --adapter bepinex",
		},
	},
}

// newBypassService builds the game-root fixture in one of bypassCases'
// configurations.
func newBypassService(t *testing.T, tc bypassCase) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := newBepInExGameRootService(t)
	tc.setup(t, svc, game)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	require.Equal(t, tc.adapterID, svc.AdapterName(game), "fixture: the game must resolve to the bypassing adapter")
	return svc, game
}

// bypassWarnings returns the warnings in ws that are about the bypass.
func bypassWarnings(ws []string) []string {
	var out []string
	for _, w := range ws {
		if strings.Contains(w, "exactly as packaged") {
			out = append(out, w)
		}
	}
	return out
}

// TestPlanImportArchive_ALoaderGameOnAnotherAdapterIsWarned is the
// reproduced path. The plan is the surface both frontends render BEFORE an
// import commits, so this is where the warning can still change the
// answer. The archive is NOT refused - design decision 11 is a warning,
// because a half-configured game is a real state - and it lays out
// exactly as packaged, which is what the warning says.
func TestPlanImportArchive_ALoaderGameOnAnotherAdapterIsWarned(t *testing.T) {
	for name, tc := range bypassCases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBypassService(t, tc)

			archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
			createImportTestZip(t, archivePath, thunderstorePackage)

			plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
			require.NoError(t, err, "the loader is there, so nothing is refused")

			files := append([]string(nil), plan.Files...)
			sort.Strings(files)
			assert.Equal(t, fromSlashAll([]string{
				"BepInEx/config/Skinwalkers.cfg",
				"BepInEx/plugins/Skinwalkers/SkinwalkerMod.dll",
				"icon.png",
				"manifest.json",
			}), files, "the game's own adapter lays nothing out - which is the harm the warning names")

			warnings := bypassWarnings(plan.Warnings)
			require.Len(t, warnings, 1, "the plan must say why its file list looks like that: %q", plan.Warnings)
			w := warnings[0]
			assert.Contains(t, w, "laid out for BepInEx")
			assert.Contains(t, w, "manifest.json")
			assert.Contains(t, w, "BepInEx/config")
			for _, want := range tc.wants {
				assert.Contains(t, w, want)
			}
		})
	}
}

// TestPlanImportArchive_TheBypassWarningNeedsABepInExArchive keeps the
// warning to archives the BepInEx rules would actually have touched: an
// asset pack into the same misconfigured game is deployed the way the
// adapter says, and nothing about BepInEx is worth saying about it.
func TestPlanImportArchive_TheBypassWarningNeedsABepInExArchive(t *testing.T) {
	svc, game := newBypassService(t, bypassCases["a declared loader under an explicit generic-files adapter"])

	archivePath := filepath.Join(t.TempDir(), "Textures-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"textures/grass.png": "png"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Empty(t, bypassWarnings(plan.Warnings))
}

// TestPlanImportArchive_ACorrectlyResolvedLoaderGameIsNotWarned is the
// other side: the same package into a declaring game with no `adapter:`
// key resolves to bepinex, lays out, and has nothing to be warned about.
func TestPlanImportArchive_ACorrectlyResolvedLoaderGameIsNotWarned(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, thunderstorePackage)

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Empty(t, bypassWarnings(plan.Warnings))
	assert.NotContains(t, plan.Files, "manifest.json")
}

// TestDownloadIngest_ALoaderGameOnAnotherAdapterIsWarned is the same
// warning on the DOWNLOAD path, which has no plan: it rides the flow's own
// event sink, as every adapter warning on that path does.
func TestDownloadIngest_ALoaderGameOnAnotherAdapterIsWarned(t *testing.T) {
	fixture := newBepInExDownloadFixture(t, thunderstorePackage, true)
	fixture.game.Adapter = "generic-files"
	require.NoError(t, fixture.svc.SaveGame(context.Background(), fixture.game))

	sink, events := core.RecordEvents()
	_, err := fixture.svc.DownloadModForTest(context.Background(), "bepinex-repo",
		fixture.game, &fixture.mod, &fixture.file, sink)
	require.NoError(t, err)

	var got []string
	for _, e := range *events {
		if w, ok := e.(core.WarningEvent); ok {
			got = append(got, w.Message)
		}
	}
	warnings := bypassWarnings(got)
	require.Len(t, warnings, 1, "events: %q", got)
	assert.Contains(t, warnings[0], `"generic-files"`)
}

// TestLoaderStatus_SaysWhenTheAdapterIgnoresTheLoader puts the same fact on
// the report `lmm game show`, GET /api/v1/games/{id} and the web loader
// panel all render, so a user inspecting the game sees it without having
// to import anything first.
func TestLoaderStatus_SaysWhenTheAdapterIgnoresTheLoader(t *testing.T) {
	for name, tc := range bypassCases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBypassService(t, tc)

			status, err := svc.LoaderStatus(context.Background(), game.ID)
			require.NoError(t, err)
			warnings := bypassWarnings(status.Warnings)
			require.Len(t, warnings, 1, "warnings: %q", status.Warnings)
			for _, want := range tc.wants {
				assert.Contains(t, warnings[0], want)
			}
		})
	}

	t.Run("a correctly resolved loader game says nothing about it", func(t *testing.T) {
		svc, game := newBepInExDeclaredService(t)
		status, err := svc.LoaderStatus(context.Background(), game.ID)
		require.NoError(t, err)
		assert.Empty(t, bypassWarnings(status.Warnings))
	})
}

// TestAdapterConfigWarnings is decision 11's load-time half: every game
// whose `loader:` block its adapter ignores, and only those. It reads no
// disk - it runs on every lmm invocation - so an installed-but-undeclared
// loader is left to the per-archive and loader-report warnings above.
func TestAdapterConfigWarnings(t *testing.T) {
	for name, tc := range bypassCases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBypassService(t, tc)
			warnings := svc.AdapterConfigWarnings()
			if game.Loader == nil {
				assert.Empty(t, warnings, "the load-time warning is about the loader BLOCK")
				return
			}
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], `game "lethal-company" declares the BepInEx loader`)
			for _, want := range tc.wants {
				assert.Contains(t, warnings[0], want)
			}
		})
	}

	t.Run("a correctly configured install warns about nothing", func(t *testing.T) {
		svc, _ := newBepInExDeclaredService(t)
		assert.Empty(t, svc.AdapterConfigWarnings())
	})
}

// TestVerify_ALoaderGameOnAnotherAdapterIsWarned is the same fact on `lmm
// verify` - the health check both frontends render, and on the web the one
// place a user looks without having asked about the loader first. A
// WARNING, not an issue: design decision 11 keeps the state allowed.
func TestVerify_ALoaderGameOnAnotherAdapterIsWarned(t *testing.T) {
	for name, tc := range bypassCases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBypassService(t, tc)
			_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
			require.NoError(t, err)

			res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
			require.NoError(t, err)
			f := findingWithStatus(res.Result, "loader_adapter_ignored")
			require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
			assert.False(t, f.Fixable, "lmm does not rewrite games.yaml")
			assert.NotEmpty(t, f.FixableReason)
			for _, want := range tc.wants {
				assert.Contains(t, f.Note, want)
			}
			assert.Positive(t, res.Result.Warnings)
		})
	}

	t.Run("a correctly resolved loader game has no such row", func(t *testing.T) {
		svc, game := newVerifyLoaderService(t, &domain.GameLoader{Kind: domain.LoaderKindBepInEx})
		bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapUnknown, time.Now())

		res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		assert.Nil(t, findingWithStatus(res.Result, "loader_adapter_ignored"))
	})
}
