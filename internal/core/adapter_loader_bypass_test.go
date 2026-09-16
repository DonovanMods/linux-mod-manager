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
// cache, with nothing said anywhere.
//
// The #413 re-review settled WHERE it is said (M1). A warning must be
// silenceable by the fix it suggests, and a persistent flag is for a
// contradiction rather than a deliberate choice:
//
//   - the PER-ARCHIVE warning fires in every bypass case, for an archive the
//     BepInEx rules would have laid out - where the harm happens;
//   - the PERSISTENT surfaces (load time, the loader report, verify) flag
//     only a `loader:` block the adapter ignores, or an installed BepInEx
//     an IMPLICIT adapter ignores. An explicit `adapter:` on a game whose
//     BepInEx is merely installed is the user's acknowledged choice.
//
// Every remedy each warning offers is applied below, and must silence it.

import (
	"context"
	"fmt"
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

// The consequence sentences, verbatim: what "exactly as packaged" costs in
// the terms a user can check in their game directory. The second is the
// mod_path-off-the-game-root one, with the mod path to fill in.
const (
	rootConsequence    = "package metadata such as manifest.json lands in the game directory, a plugin is not moved under BepInEx/, and a BepInEx/config file is linked from the shared mod cache instead of copied once, so editing it edits every profile's copy."
	nonRootConsequence = "every path in an archive is joined onto %[2]s, so package metadata such as manifest.json lands there too, and an archive's own BepInEx/ tree - its BepInEx/config files included - is nested under it, where BepInEx does not read it."
)

// bypassRemedy is one fix a warning offers, and how a user applies it.
type bypassRemedy struct {
	// offers is the remedy's text, which the warning must contain.
	offers string
	// perArchive: the per-archive warning offers it too (and it must
	// silence that one as well). The acknowledgements are persistent-only:
	// they leave the game on its adapter, which is what the per-archive
	// warning is about.
	perArchive bool
	apply      func(t *testing.T, svc *core.Service, id string)
}

// bypassCase is one way a loader game ends up on another adapter.
type bypassCase struct {
	adapterID string // what the game resolves to instead of bepinex
	setup     func(t *testing.T, svc *core.Service, game *domain.Game)
	// persistent is the loader-report / verify / after-edit sentence, or ""
	// for an acknowledged configuration that none of them flags. %[1]s is
	// the install path, %[2]s the mod path.
	persistent string
	// loadTime is the one-line load-time warning (#456), for a case that
	// declares the loader and is persistent.
	loadTime string
	// archive is the per-archive sentence, with the same verbs.
	archive  string
	remedies []bypassRemedy
}

// The remedies, as a user would apply them.
var (
	remedyAdapterBepInEx = bypassRemedy{
		offers: "Run `lmm game edit lethal-company --adapter bepinex` to have lmm lay BepInEx archives out", perArchive: true,
		apply: func(t *testing.T, svc *core.Service, id string) { setAdapter(t, svc, id, "bepinex") },
	}
	remedyUnloadExplicit = bypassRemedy{
		offers: "remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`)",
		apply:  func(t *testing.T, svc *core.Service, id string) { unload(t, svc, id) },
	}
	remedyDropCompile = bypassRemedy{
		offers: "remove `deploy_mode: compile` from games.yaml", perArchive: true,
		apply: func(t *testing.T, svc *core.Service, id string) {
			editGame(t, svc, id, func(g *domain.Game) { g.DeployMode = domain.DeployExtract })
		},
	}
	remedyDropCompileAndKey = bypassRemedy{
		offers: "remove `deploy_mode: compile` and the `adapter:` key from games.yaml", perArchive: true,
		apply: func(t *testing.T, svc *core.Service, id string) {
			editGame(t, svc, id, func(g *domain.Game) { g.DeployMode, g.Adapter = domain.DeployExtract, "" })
		},
	}
	remedyMoveToGameRoot = bypassRemedy{
		offers: "run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`", perArchive: true,
		apply: func(t *testing.T, svc *core.Service, id string) { setModPathToRoot(t, svc, id) },
	}
	remedyMoveToGameRootAndBepInEx = bypassRemedy{
		offers: "run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm game edit lethal-company --adapter bepinex`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`", perArchive: true,
		apply: func(t *testing.T, svc *core.Service, id string) {
			setModPathToRoot(t, svc, id)
			setAdapter(t, svc, id, "bepinex")
		},
	}
)

// remedyPin keeps the game on adapterID and drops the loader block when it
// has one: the acknowledgement for an IMPLICIT adapter.
func remedyPin(adapterID string, declared bool) bypassRemedy {
	offers := fmt.Sprintf("or, to keep this game on %q, pin the adapter (`lmm game edit lethal-company --adapter %s`)", adapterID, adapterID)
	if declared {
		offers = fmt.Sprintf("or, to keep this game on %q, remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`) and pin the adapter (`lmm game edit lethal-company --adapter %s`)", adapterID, adapterID)
	}
	return bypassRemedy{offers: offers, apply: func(t *testing.T, svc *core.Service, id string) {
		if declared {
			unload(t, svc, id)
		}
		setAdapter(t, svc, id, adapterID)
	}}
}

func setAdapter(t *testing.T, svc *core.Service, id, name string) {
	t.Helper()
	_, err := svc.SetGameAdapter(context.Background(), id, name)
	require.NoError(t, err)
}

// setModPathToRoot is `lmm game edit <id> --mod-path <install path>` (#456:
// the step used to be a hand edit of games.yaml).
func setModPathToRoot(t *testing.T, svc *core.Service, id string) {
	t.Helper()
	game, err := svc.GetGame(id)
	require.NoError(t, err)
	_, err = svc.SetGameModPath(context.Background(), id, game.InstallPath)
	require.NoError(t, err)
}

func unload(t *testing.T, svc *core.Service, id string) {
	t.Helper()
	_, err := svc.UpdateGameLoader(context.Background(), id, nil)
	require.NoError(t, err)
}

// editGame is a hand edit of games.yaml: the keys lmm has no command for.
func editGame(t *testing.T, svc *core.Service, id string, edit func(*domain.Game)) {
	t.Helper()
	current, err := svc.GetGame(id)
	require.NoError(t, err)
	updated := *current
	edit(&updated)
	require.NoError(t, svc.SaveGame(context.Background(), &updated))
}

// Setup steps the cases compose.
func declare(g *domain.Game) { g.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx} }

func install(t *testing.T, g *domain.Game) {
	bepinexInstall(t, g.InstallPath, "", domain.LoaderBootstrapUnknown, time.Time{})
}

func compile(svc *core.Service, g *domain.Game) {
	svc.RegisterAdapter(icarus.New())
	g.DeployMode = domain.DeployCompile
}

func pluginsModPath(g *domain.Game) { g.ModPath = filepath.Join(g.InstallPath, "BepInEx", "plugins") }

// The sentences' two openings and the per-archive one.
const (
	declaredOpening  = `game "lethal-company" declares the BepInEx loader, but %s, so lmm ignores the loader block and deploys BepInEx archives exactly as packaged: `
	installedOpening = `BepInEx is installed in game "lethal-company"'s directory, but %s, so lmm does not act on it and deploys BepInEx archives exactly as packaged: `
	archiveOpening   = `this archive is laid out for BepInEx (game-root-relative), but %s, so lmm deploys it exactly as packaged: `
)

// compileLead is the enable remedy's lead-in for a compile game.
const compileLead = "`deploy_mode: compile` needs an adapter that compiles, which bepinex is not; if lethal-company does not compile its mods, then to have lmm lay BepInEx archives out, "

// nonRootWhy is why a mod_path off the game root resolves to the identity.
const nonRootWhy = `, because its mod_path (%[2]s) is not its install path and a BepInEx layout is relative to the game root`

var bypassCases = map[string]bypassCase{
	"declared, explicit generic-files": {
		loadTime:  `game "lethal-company" declares the BepInEx loader, but lmm ignores it because its adapter is "generic-files"; run ` + "`lmm game show lethal-company`" + ` for the fix`,
		adapterID: "generic-files",
		setup:     func(t *testing.T, _ *core.Service, g *domain.Game) { declare(g); g.Adapter = "generic-files" },
		persistent: fmt.Sprintf(declaredOpening, `its adapter is "generic-files"`) + rootConsequence +
			" Run `lmm game edit lethal-company --adapter bepinex` to have lmm lay BepInEx archives out; or, if \"generic-files\" is the adapter you meant, remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`).",
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "generic-files"`) + rootConsequence +
			" Run `lmm game edit lethal-company --adapter bepinex` to have lmm lay BepInEx archives out.",
		remedies: []bypassRemedy{remedyAdapterBepInEx, remedyUnloadExplicit},
	},
	"declared and installed, explicit generic-files": {
		loadTime:  `game "lethal-company" declares the BepInEx loader, but lmm ignores it because its adapter is "generic-files"; run ` + "`lmm game show lethal-company`" + ` for the fix`,
		adapterID: "generic-files",
		setup: func(t *testing.T, _ *core.Service, g *domain.Game) {
			declare(g)
			install(t, g)
			g.Adapter = "generic-files"
		},
		persistent: fmt.Sprintf(declaredOpening, `its adapter is "generic-files"`) + rootConsequence +
			" Run `lmm game edit lethal-company --adapter bepinex` to have lmm lay BepInEx archives out; or, if \"generic-files\" is the adapter you meant, remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`).",
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "generic-files"`) + rootConsequence +
			" Run `lmm game edit lethal-company --adapter bepinex` to have lmm lay BepInEx archives out.",
		remedies: []bypassRemedy{remedyAdapterBepInEx, remedyUnloadExplicit},
	},
	"declared, deploy_mode: compile": {
		loadTime:  `game "lethal-company" declares the BepInEx loader, but lmm ignores it because ` + "`deploy_mode: compile` selects the \"icarus\" adapter; run `lmm game show lethal-company` for the fix",
		adapterID: "icarus",
		setup:     func(t *testing.T, svc *core.Service, g *domain.Game) { declare(g); compile(svc, g) },
		persistent: fmt.Sprintf(declaredOpening, "its adapter is \"icarus\", which `deploy_mode: compile` selects") + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` from games.yaml; or, to keep this game on \"icarus\", remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`) and pin the adapter (`lmm game edit lethal-company --adapter icarus`).",
		archive: fmt.Sprintf(archiveOpening, "game \"lethal-company\"'s adapter is \"icarus\", which `deploy_mode: compile` selects") + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` from games.yaml.",
		remedies: []bypassRemedy{remedyDropCompile, remedyPin("icarus", true)},
	},
	"declared, explicit icarus with deploy_mode: compile": {
		loadTime:  `game "lethal-company" declares the BepInEx loader, but lmm ignores it because its adapter is "icarus"; run ` + "`lmm game show lethal-company`" + ` for the fix`,
		adapterID: "icarus",
		setup: func(t *testing.T, svc *core.Service, g *domain.Game) {
			declare(g)
			compile(svc, g)
			g.Adapter = "icarus"
		},
		persistent: fmt.Sprintf(declaredOpening, `its adapter is "icarus"`) + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` and the `adapter:` key from games.yaml; or, if \"icarus\" is the adapter you meant, remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`).",
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "icarus"`) + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` and the `adapter:` key from games.yaml.",
		remedies: []bypassRemedy{remedyDropCompileAndKey, remedyUnloadExplicit},
	},
	// An IMPLICIT adapter ignoring an installed BepInEx: nobody chose it.
	"installed, deploy_mode: compile": {
		adapterID: "icarus",
		setup:     func(t *testing.T, svc *core.Service, g *domain.Game) { install(t, g); compile(svc, g) },
		persistent: fmt.Sprintf(installedOpening, "its adapter is \"icarus\", which `deploy_mode: compile` selects") + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` from games.yaml; or, to keep this game on \"icarus\", pin the adapter (`lmm game edit lethal-company --adapter icarus`).",
		archive: fmt.Sprintf(archiveOpening, "game \"lethal-company\"'s adapter is \"icarus\", which `deploy_mode: compile` selects") + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` from games.yaml.",
		remedies: []bypassRemedy{remedyDropCompile, remedyPin("icarus", false)},
	},
	// ACKNOWLEDGED: an explicit adapter on a game whose BepInEx is merely
	// installed. Only the per-archive warning speaks.
	"installed, explicit generic-files": {
		adapterID: "generic-files",
		setup:     func(t *testing.T, _ *core.Service, g *domain.Game) { install(t, g); g.Adapter = "generic-files" },
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "generic-files"`) + rootConsequence +
			" Run `lmm game edit lethal-company --adapter bepinex` to have lmm lay BepInEx archives out.",
		remedies: []bypassRemedy{remedyAdapterBepInEx},
	},
	"installed, explicit icarus with deploy_mode: compile": {
		adapterID: "icarus",
		setup: func(t *testing.T, svc *core.Service, g *domain.Game) {
			install(t, g)
			compile(svc, g)
			g.Adapter = "icarus"
		},
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "icarus"`) + rootConsequence +
			" " + compileLead + "remove `deploy_mode: compile` and the `adapter:` key from games.yaml.",
		remedies: []bypassRemedy{remedyDropCompileAndKey},
	},
	// #413 re-review P-b: the v1 configuration. No key chose the identity -
	// the mod_path did - so it is flagged, with both ways out.
	"installed, mod_path in BepInEx/plugins": {
		adapterID: "generic-files",
		setup:     func(t *testing.T, _ *core.Service, g *domain.Game) { install(t, g); pluginsModPath(g) },
		persistent: fmt.Sprintf(installedOpening, `its adapter is "generic-files"`+nonRootWhy) + nonRootConsequence +
			" To have lmm lay BepInEx archives out, run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`, which moves what is already imported under BepInEx/; or, to keep this game on \"generic-files\", pin the adapter (`lmm game edit lethal-company --adapter generic-files`).",
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "generic-files"`+nonRootWhy) + nonRootConsequence +
			" To have lmm lay BepInEx archives out, run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`, which moves what is already imported under BepInEx/.",
		remedies: []bypassRemedy{remedyMoveToGameRoot, remedyPin("generic-files", false)},
	},
	"declared, mod_path in BepInEx/plugins": {
		loadTime:  `game "lethal-company" declares the BepInEx loader, but lmm ignores it because its mod_path is not the game root; run ` + "`lmm game show lethal-company`" + ` for the fix`,
		adapterID: "generic-files",
		setup:     func(t *testing.T, _ *core.Service, g *domain.Game) { declare(g); pluginsModPath(g) },
		persistent: fmt.Sprintf(declaredOpening, `its adapter is "generic-files"`+nonRootWhy) + nonRootConsequence +
			" To have lmm lay BepInEx archives out, run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`, which moves what is already imported under BepInEx/; or, to keep this game on \"generic-files\", remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`) and pin the adapter (`lmm game edit lethal-company --adapter generic-files`).",
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "generic-files"`+nonRootWhy) + nonRootConsequence +
			" To have lmm lay BepInEx archives out, run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`, which moves what is already imported under BepInEx/.",
		remedies: []bypassRemedy{remedyMoveToGameRoot, remedyPin("generic-files", true)},
	},
	"declared and installed, explicit generic-files, mod_path in BepInEx/plugins": {
		loadTime:  `game "lethal-company" declares the BepInEx loader, but lmm ignores it because its adapter is "generic-files"; run ` + "`lmm game show lethal-company`" + ` for the fix`,
		adapterID: "generic-files",
		setup: func(t *testing.T, _ *core.Service, g *domain.Game) {
			declare(g)
			install(t, g)
			pluginsModPath(g)
			g.Adapter = "generic-files"
		},
		persistent: fmt.Sprintf(declaredOpening, `its adapter is "generic-files"`) + nonRootConsequence +
			" To have lmm lay BepInEx archives out, run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm game edit lethal-company --adapter bepinex`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`, which moves what is already imported under BepInEx/; or, if \"generic-files\" is the adapter you meant, remove the `loader:` block (`lmm game edit lethal-company --loader \"\"`).",
		archive: fmt.Sprintf(archiveOpening, `game "lethal-company"'s adapter is "generic-files"`) + nonRootConsequence +
			" To have lmm lay BepInEx archives out, run `lmm purge --game lethal-company`, then run `lmm game edit lethal-company --mod-path %[1]s`, then run `lmm game edit lethal-company --adapter bepinex`, then run `lmm deploy --game lethal-company` and `lmm verify --fix --game lethal-company`, which moves what is already imported under BepInEx/.",
		remedies: []bypassRemedy{remedyMoveToGameRootAndBepInEx, remedyUnloadExplicit},
	},
}

// newBypassService builds the game-root fixture in one of bypassCases'
// configurations.
func newBypassService(t *testing.T, tc bypassCase) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := newBepInExGameRootService(t)
	tc.setup(t, svc, game)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)
	require.Equal(t, tc.adapterID, svc.AdapterName(game), "fixture: the game must resolve to the bypassing adapter")
	return svc, game
}

// fill expands a case's %[1]s (install path) and %[2]s (mod path).
func fill(format string, game *domain.Game) string {
	if !strings.Contains(format, "%[") {
		return format
	}
	return fmt.Sprintf(format, game.InstallPath, game.ModPath)
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

// persistentWarnings is every persistent surface that says the whole
// sentence for game, in order: the one-game query, the loader report and
// the verify row. The load-time list says a short one (loadTimeWarnings).
func persistentWarnings(t *testing.T, svc *core.Service, id string) []string {
	t.Helper()
	var got []string
	if w := svc.AdapterConfigWarning(id); w != "" {
		got = append(got, w)
	}
	status, err := svc.LoaderStatus(context.Background(), id)
	require.NoError(t, err)
	got = append(got, bypassWarnings(status.Warnings)...)

	game, err := svc.GetGame(id)
	require.NoError(t, err)
	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	if f := findingWithStatus(res.Result, "loader_adapter_ignored"); f != nil {
		got = append(got, f.Note)
	}
	return got
}

// loadTimeWarnings is the load-time list's lines about game id.
func loadTimeWarnings(svc *core.Service, id string) []string {
	var got []string
	for _, w := range svc.AdapterConfigWarnings() {
		if strings.Contains(w, `"`+id+`"`) {
			got = append(got, w)
		}
	}
	return got
}

// archiveWarnings plans the Thunderstore package into id and returns the
// plan's bypass warnings.
func archiveWarnings(t *testing.T, svc *core.Service, id string) []string {
	t.Helper()
	game, err := svc.GetGame(id)
	require.NoError(t, err)
	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, thunderstorePackage)
	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err, "the loader is there, so nothing is refused")
	return bypassWarnings(plan.Warnings)
}

// TestLoaderBypass_EachSurfaceSaysItsSentence pins the sentences whole -
// the fragments the earlier tests asserted let a garbled one ("but game
// "valheim" its adapter is ...") through - and where each one is said.
func TestLoaderBypass_EachSurfaceSaysItsSentence(t *testing.T) {
	for name, tc := range bypassCases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBypassService(t, tc)

			persistent := persistentWarnings(t, svc, game.ID)
			if tc.persistent == "" {
				assert.Empty(t, persistent, "an explicit adapter on an installed BepInEx is an acknowledged choice")
			} else {
				want := fill(tc.persistent, game)
				require.Len(t, persistent, 3, "one-game query, loader report, verify row: %q", persistent)
				for _, got := range persistent {
					assert.Equal(t, want, got)
				}
			}

			// #456: the load-time list is printed by every command, so it is
			// one short sentence pointing at `lmm game show`, where the whole
			// one is. It reads no disk, so it has only declared games.
			loadTime := loadTimeWarnings(svc, game.ID)
			if tc.persistent == "" || !game.DeclaresBepInEx() {
				assert.Empty(t, loadTime)
			} else {
				require.Len(t, loadTime, 1)
				assert.Equal(t, tc.loadTime, loadTime[0])
			}

			archive := archiveWarnings(t, svc, game.ID)
			require.Len(t, archive, 1, "the per-archive warning fires in every bypass case")
			assert.Equal(t, fill(tc.archive, game), archive[0])
		})
	}
}

// TestLoaderBypass_EveryRemedySilencesItsWarning applies each remedy a
// warning offers, as a user would, and requires the warning to stop - the
// persistent one always, the per-archive one when it offered the remedy.
func TestLoaderBypass_EveryRemedySilencesItsWarning(t *testing.T) {
	for name, tc := range bypassCases {
		for _, remedy := range tc.remedies {
			t.Run(name+"/"+remedy.offers, func(t *testing.T) {
				svc, game := newBypassService(t, tc)
				offers := fill(remedy.offers, game)
				if tc.persistent != "" {
					assert.Contains(t, fill(tc.persistent, game), offers, "the persistent warning offers this remedy")
				}
				if remedy.perArchive {
					assert.Contains(t, fill(tc.archive, game), offers, "the per-archive warning offers this remedy")
				} else {
					assert.NotContains(t, fill(tc.archive, game), offers, "an acknowledgement does not silence the per-archive warning, so it must not offer it")
				}

				remedy.apply(t, svc, game.ID)

				assert.Empty(t, persistentWarnings(t, svc, game.ID), "the remedy must silence the persistent warning")
				assert.Empty(t, loadTimeWarnings(svc, game.ID), "the remedy must silence the load-time line")
				if remedy.perArchive {
					assert.Empty(t, archiveWarnings(t, svc, game.ID), "the remedy must silence the per-archive warning")
				}
			})
		}
	}
}

// TestLoaderBypass_NotForAConfigurationEveryFlowRefuses (#413 re-review
// L2): "lmm deploys BepInEx archives exactly as packaged" is false for a
// game lmm refuses to deploy anything to, and the refusal already names
// the fix. So there is no bypass warning at all.
func TestLoaderBypass_NotForAConfigurationEveryFlowRefuses(t *testing.T) {
	cases := map[string]func(svc *core.Service, g *domain.Game){
		"an adapter this build does not ship": func(_ *core.Service, g *domain.Game) { declare(g); g.Adapter = "bepinx" },
		"deploy_mode: compile with an adapter that cannot compile": func(svc *core.Service, g *domain.Game) {
			declare(g)
			compile(svc, g)
			g.Adapter = "generic-files"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBepInExGameRootService(t)
			setup(svc, game)
			install(t, game)
			require.NoError(t, svc.SaveGame(context.Background(), game))
			_, err := svc.AdapterFor(game)
			require.Error(t, err, "fixture: every flow refuses this game")

			assert.Empty(t, svc.AdapterConfigWarnings())
			assert.Empty(t, svc.AdapterConfigWarning(game.ID))
			status, err := svc.LoaderStatus(context.Background(), game.ID)
			require.NoError(t, err)
			assert.Empty(t, bypassWarnings(status.Warnings))

			archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
			createImportTestZip(t, archivePath, thunderstorePackage)
			_, err = svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
			require.Error(t, err, "the plan is refused, and says why itself")
			assert.NotContains(t, err.Error(), "exactly as packaged")
		})
	}
}

// TestPlanImportArchive_ALoaderGameOnAnotherAdapterDeploysAsPackaged is the
// reproduced path. The archive is NOT refused - design decision 11 is a
// warning, because a half-configured game is a real state - and it lays
// out exactly as packaged, which is what the warning says.
func TestPlanImportArchive_ALoaderGameOnAnotherAdapterDeploysAsPackaged(t *testing.T) {
	svc, game := newBypassService(t, bypassCases["declared, explicit generic-files"])

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, thunderstorePackage)
	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)

	files := append([]string(nil), plan.Files...)
	sort.Strings(files)
	assert.Equal(t, fromSlashAll([]string{
		"BepInEx/config/Skinwalkers.cfg",
		"BepInEx/plugins/Skinwalkers/SkinwalkerMod.dll",
		"icon.png",
		"manifest.json",
	}), files, "the game's own adapter lays nothing out - which is the harm the warning names")
}

// TestPlanImportArchive_TheBypassWarningNeedsABepInExArchive keeps the
// warning to archives the BepInEx rules would actually have touched: an
// asset pack into the same misconfigured game is deployed the way the
// adapter says, and nothing about BepInEx is worth saying about it.
func TestPlanImportArchive_TheBypassWarningNeedsABepInExArchive(t *testing.T) {
	svc, game := newBypassService(t, bypassCases["declared, explicit generic-files"])

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

// TestImportArchive_TheBypassWarningReachesTheResultOnce (#413 re-review
// L7): the plan carries the per-archive warning, the apply copies the
// plan's warnings into its result and its event stream, and the ingest
// adds no second copy of its own.
func TestImportArchive_TheBypassWarningReachesTheResultOnce(t *testing.T) {
	svc, game := newBypassService(t, bypassCases["installed, explicit generic-files"])

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, thunderstorePackage)
	sink, events := core.RecordEvents()
	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{Force: true}, sink)
	require.NoError(t, err)
	assert.Len(t, bypassWarnings(result.Warnings), 1, "result warnings: %q", result.Warnings)

	var said []string
	for _, e := range *events {
		if step, ok := e.(core.StepEvent); ok && step.Phase == core.ImportArchiveWarning {
			said = append(said, step.Detail)
		}
	}
	assert.Len(t, bypassWarnings(said), 1, "the readout says it once: %q", said)
}

// TestDownloadIngest_ALoaderGameOnAnotherAdapterIsWarned is the same
// warning on the DOWNLOAD path, which has no plan: it rides the flow's own
// event sink, as every adapter warning on that path does - including for
// the acknowledged configuration, which only the per-archive warning
// reaches.
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
	assert.Contains(t, warnings[0], `game "`+fixture.game.ID+`"'s adapter is "generic-files"`)
}

// TestLoaderBypass_ACorrectlyConfiguredGameSaysNothing: a game resolving to
// bepinex, declared or installed, is told nothing about a bypass anywhere.
func TestLoaderBypass_ACorrectlyConfiguredGameSaysNothing(t *testing.T) {
	cases := map[string]func(t *testing.T, g *domain.Game){
		"declared":                  func(_ *testing.T, g *domain.Game) { declare(g) },
		"installed":                 func(t *testing.T, g *domain.Game) { install(t, g) },
		"declared, adapter bepinex": func(_ *testing.T, g *domain.Game) { declare(g); g.Adapter = "bepinex" },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBepInExGameRootService(t)
			setup(t, game)
			require.NoError(t, svc.SaveGame(context.Background(), game))
			_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
			require.NoError(t, err)
			require.Equal(t, "bepinex", svc.AdapterName(game))

			assert.Empty(t, persistentWarnings(t, svc, game.ID))
			assert.Empty(t, archiveWarnings(t, svc, game.ID))
		})
	}
}

// TestVerify_ALoaderGameOnAnotherAdapterIsAWarningRow: the verify row is a
// WARNING - design decision 11 keeps the state allowed - that --fix cannot
// clear, and it counts toward the tally the web Health card shows.
func TestVerify_ALoaderGameOnAnotherAdapterIsAWarningRow(t *testing.T) {
	svc, game := newBypassService(t, bypassCases["declared, explicit generic-files"])

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_adapter_ignored")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.False(t, f.Fixable, "lmm does not rewrite games.yaml")
	assert.NotEmpty(t, f.FixableReason)
	assert.Positive(t, res.Result.Warnings)

	t.Run("an acknowledged configuration has no row and adds nothing to the tally", func(t *testing.T) {
		svc, game := newBypassService(t, bypassCases["installed, explicit generic-files"])
		res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
		require.NoError(t, err)
		assert.Nil(t, findingWithStatus(res.Result, "loader_adapter_ignored"))
		assert.Zero(t, res.Result.Warnings, "statuses were %v", findingStatuses(res.Result))
	})
}
