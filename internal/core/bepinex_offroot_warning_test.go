package core_test

// #464: a BepInEx-shaped archive going into a game whose adapter is the
// identity because its mod_path is not its install path. The issue feared
// the plugin deployed there silently, where BepInEx does not load it. It
// never is silent, on any path an archive reaches the cache by:
//
//   - with no BepInEx at all, the archive is refused (#359) and the refusal
//     names `lmm game edit <id> --loader bepinex`;
//   - with BepInEx installed, declared or not, the archive deploys exactly
//     as packaged and the per-archive warning says so, with the steps that
//     make lmm lay it out - live on the event stream, and in the flow's
//     --json document, which a --json run (no event sink at all) reads.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// offRootState is how much BepInEx a game whose mod_path is not its
// install path has.
type offRootState string

const (
	offRootNoLoader   offRootState = "no loader"
	offRootInstalled  offRootState = "installed, undeclared"
	offRootDeclared   offRootState = "installed and declared"
	offRootWarningKey              = "this archive is laid out for BepInEx"
)

var offRootStates = []offRootState{offRootNoLoader, offRootInstalled, offRootDeclared}

// offRootGame reshapes fixture's game into state, with mod_path under the
// install path rather than at it, and saves it.
func offRootGame(t *testing.T, fixture *bepinexDownloadFixture, state offRootState) {
	t.Helper()
	game := fixture.game
	game.ModPath = filepath.Join(game.InstallPath, "mods")
	game.Loader = nil
	if state != offRootNoLoader {
		preloader := filepath.Join(game.InstallPath, filepath.FromSlash(domain.BepInExPreloaderPath))
		require.NoError(t, os.MkdirAll(filepath.Dir(preloader), 0o755))
		require.NoError(t, os.WriteFile(preloader, []byte("preloader"), 0o644))
	}
	if state == offRootDeclared {
		game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
	}
	require.NoError(t, fixture.svc.SaveGame(context.Background(), game))
	require.Equal(t, "generic-files", fixture.svc.AdapterName(game), "the game resolves to the identity")
}

var offRootArchive = map[string]string{"BepInEx/plugins/Thing.dll": "assembly"}

// warningsIn collects every WarningEvent message a flow emitted.
func warningsIn(events *[]string) core.EventSink {
	return func(e core.Event) {
		if w, ok := e.(core.WarningEvent); ok {
			*events = append(*events, w.Message)
		}
	}
}

// containsLine reports whether some line mentions every part.
func containsLine(lines []string, parts ...string) bool {
	for _, line := range lines {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(line, p)
		}
		if all {
			return true
		}
	}
	return false
}

// assertOffRootOutcome checks one path's outcome for state: a refusal
// naming the loader edit, or a deploy as packaged that warned both live
// and in the document - when there is one to check (checkDocument).
func assertOffRootOutcome(t *testing.T, state offRootState, err error, live []string, checkDocument bool, document []string) {
	t.Helper()
	if state == offRootNoLoader {
		var loaderErr *core.LoaderRequiredError
		require.ErrorAs(t, err, &loaderErr)
		assert.Contains(t, err.Error(), "--loader bepinex")
		return
	}
	require.NoError(t, err)
	assert.True(t, containsLine(live, offRootWarningKey, "--mod-path"), "the warning is on the event stream: %q", live)
	if checkDocument {
		assert.True(t, containsLine(document, offRootWarningKey, "--mod-path"), "and in the --json document: %q", document)
	}
}

func TestOffRootBepInExArchive_Install(t *testing.T) {
	for _, state := range offRootStates {
		for _, withSink := range []bool{true, false} {
			name := string(state) + "/live"
			if !withSink {
				// A --json run passes no sink: the document alone carries it.
				name = string(state) + "/json"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				fixture := newBepInExDownloadFixture(t, offRootArchive, false)
				offRootGame(t, fixture, state)

				plan, err := fixture.svc.PlanInstall(ctx, fixture.game, "default", "bepinex-repo", fixture.mod.ID, false)
				require.NoError(t, err)
				var live []string
				var sink core.EventSink
				if withSink {
					sink = warningsIn(&live)
				}
				result, err := fixture.svc.ApplyInstall(ctx, fixture.game, plan, core.InstallOptions{}, sink)
				var document []string
				if result != nil {
					document = result.Warnings
				}
				if !withSink && state != offRootNoLoader {
					require.NoError(t, err)
					assert.True(t, containsLine(document, offRootWarningKey, "--mod-path"), "%q", document)
					return
				}
				assertOffRootOutcome(t, state, err, live, true, document)
				if state != offRootNoLoader {
					assert.FileExists(t, filepath.Join(fixture.game.ModPath, "BepInEx", "plugins", "Thing.dll"), "deployed exactly as packaged")
				}
			})
		}
	}
}

func TestOffRootBepInExArchive_Download(t *testing.T) {
	for _, state := range offRootStates {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			fixture := newBepInExDownloadFixture(t, offRootArchive, false)
			offRootGame(t, fixture, state)

			var live []string
			_, err := fixture.svc.DownloadModForTest(ctx, "bepinex-repo", fixture.game, &fixture.mod, &fixture.file, warningsIn(&live))
			assertOffRootOutcome(t, state, err, live, false, nil)
			if state == offRootNoLoader {
				return
			}

			// A deploy that has to fetch the mod again reports it in its
			// own document too.
			plan, err := fixture.svc.PlanInstall(ctx, fixture.game, "default", "bepinex-repo", fixture.mod.ID, false)
			require.NoError(t, err)
			_, err = fixture.svc.ApplyInstall(ctx, fixture.game, plan, core.InstallOptions{}, nil)
			require.NoError(t, err)
			require.NoError(t, fixture.svc.GetGameCache(fixture.game).Delete(fixture.game.ID, fixture.mod.SourceID, fixture.mod.ID, fixture.mod.Version))
			live = nil
			result, err := fixture.svc.DeployProfile(ctx, fixture.game, "default", core.DeployOptions{}, warningsIn(&live))
			require.NoError(t, err)
			require.Equal(t, 1, result.Deployed, "%+v", result)
			assertOffRootOutcome(t, state, nil, live, true, result.Warnings)
		})
	}
}

func TestOffRootBepInExArchive_Import(t *testing.T) {
	for _, state := range offRootStates {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			fixture := newBepInExDownloadFixture(t, offRootArchive, false)
			offRootGame(t, fixture, state)
			archivePath := filepath.Join(t.TempDir(), "Thing-1.0.0.zip")
			createImportTestZip(t, archivePath, offRootArchive)

			plan, err := fixture.svc.PlanImportArchive(ctx, fixture.game, "default", archivePath, core.ImportArchiveOptions{})
			if state == offRootNoLoader {
				assertOffRootOutcome(t, state, err, nil, false, nil)
				return
			}
			require.NoError(t, err)
			assert.True(t, containsLine(plan.Warnings, offRootWarningKey), "the plan both frontends show says it: %q", plan.Warnings)

			// The import readout renders the plan's warnings; the result
			// carries them for a --json run.
			result, err := fixture.svc.ApplyImportArchive(ctx, fixture.game, "default", plan, core.ImportArchiveOptions{}, nil)
			require.NoError(t, err)
			assert.True(t, containsLine(result.Warnings, offRootWarningKey, "--mod-path"), "%q", result.Warnings)
			assert.FileExists(t, filepath.Join(fixture.game.ModPath, "BepInEx", "plugins", "Thing.dll"), "deployed exactly as packaged")
			assert.False(t, strings.Contains(strings.Join(result.Warnings, "\n"), "needs the BepInEx mod loader"))
		})
	}
}
