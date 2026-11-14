package core_test

// shared_original_test.go pins #632: two mods shipping the same file over
// an original that is already there - a stock game file, or one the user
// put there. ENB and ReShade both ship d3d11.dll and dxgi.dll to the game
// root (#629), so this is an ordinary setup, under every deploy root and
// every link method.
//
// The first mod to deploy the path captures the original, as a single mod
// does; the second is an ordinary file conflict, decided by load order and
// reported by `lmm conflicts`; a purge puts the original back; and nothing
// claims the user replaced a file lmm never deployed.

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

// sharedOriginalCase is one of #632's two deploy roots: where the shared
// file lands, and the originals store's root for it.
type sharedOriginalCase struct {
	name   string
	member string // the cache member both mods ship
	root   string // the originals manifest's root
	rel    string // the manifest's relative path
	dst    func(g *domain.Game) string
}

var sharedOriginalCases = []sharedOriginalCase{
	{
		name: "game_root", member: "d3d11.dll", root: "install_path", rel: "d3d11.dll",
		dst: func(g *domain.Game) string { return filepath.Join(g.InstallPath, "d3d11.dll") },
	},
	{
		name: "mod_path", member: "textures/shared.dds", root: "mod_path", rel: "textures/shared.dds",
		dst: func(g *domain.Game) string { return filepath.Join(g.ModPath, "textures", "shared.dds") },
	},
}

// seedSharedOriginal seeds two installed, enabled mods - "first" then
// "second" in load order - that both ship c.member, over an original
// already at its deployed path.
func seedSharedOriginal(t *testing.T, svc *core.Service, game *domain.Game, c sharedOriginalCase) string {
	t.Helper()
	dst := c.dst(game)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, []byte("the game's own file"), 0o644))
	seedNamedInstalledMod(t, svc, game, "src", "first", "First", "1.0", true,
		map[string][]byte{c.member: []byte("the first mod's file")})
	seedNamedInstalledMod(t, svc, game, "src", "second", "Second", "1.0", true,
		map[string][]byte{c.member: []byte("the second mod's file")})
	seedProfileWithMod(t, svc, game.ID, "default", "src", "first", "1.0")
	seedProfileWithMod(t, svc, game.ID, "default", "src", "second", "1.0")
	return dst
}

func assertNoReplacedLinkClaim(t *testing.T, msgs []string, what string) {
	t.Helper()
	for _, m := range msgs {
		assert.NotContains(t, m, "you replaced lmm's link", "%s: lmm never deployed that path, so the user replaced nothing", what)
		assert.NotContains(t, m, "was not replaced", "%s: the original is captured, not left in place", what)
	}
}

func TestSharedPathOverAnOriginal_DeployCapturesItAndPurgeRestoresIt(t *testing.T) {
	for _, c := range sharedOriginalCases {
		for _, method := range rootMethods {
			t.Run(c.name+"/"+method.String(), func(t *testing.T) {
				ctx := context.Background()
				svc, dataDir := newOriginalsService(t)
				game := newRootGame(t, svc, method)
				dst := seedSharedOriginal(t, svc, game, c)

				res, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)
				assertNoReplacedLinkClaim(t, append(res.Warnings, res.Notes...), "deploy")

				assert.Equal(t, "the second mod's file", readFileString(t, dst),
					"the later mod in load order wins the shared path")

				rows := readOriginalsManifest(t, dataDir, game.ID)
				require.Len(t, rows, 1, "the original was captured, once")
				assert.Equal(t, c.root, rows[0].Root)
				assert.Equal(t, c.rel, rows[0].RelativePath)
				assert.Equal(t, "the game's own file", readStoredOriginal(t, dataDir, game.ID, c.root, c.rel))

				// A second deploy changes nothing and says nothing false.
				res, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)
				assertNoReplacedLinkClaim(t, append(res.Warnings, res.Notes...), "redeploy")
				assert.Equal(t, "the second mod's file", readFileString(t, dst))
				require.Len(t, readOriginalsManifest(t, dataDir, game.ID), 1)

				// The second mod is an ordinary file conflict.
				conflicts, err := svc.GetProfileConflicts(ctx, game, "default")
				require.NoError(t, err)
				require.True(t, conflictsName(conflicts, c.rel), "lmm conflicts reports the shared path: %+v", conflicts)

				// Verify sees lmm's own files there, not the user's.
				vr, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{Force: true}, nil)
				require.NoError(t, err)
				for _, f := range vr.Result.Findings {
					assertNoReplacedLinkClaim(t, []string{f.Note}, "verify")
				}

				plan, err := svc.PlanPurge(ctx, game, "default", core.PurgeOptions{})
				require.NoError(t, err)
				for _, k := range plan.Kept {
					assert.NotEqual(t, c.rel, k.Path, "purge removes lmm's file there: %+v", k)
				}
				pres, err := svc.ApplyPurge(ctx, game, plan, core.PurgeOptions{}, nil)
				require.NoError(t, err)
				assertNoReplacedLinkClaim(t, pres.Warnings, "purge")

				assert.Equal(t, "the game's own file", readFileString(t, dst), "purge puts the original back")
				assert.Empty(t, readOriginalsManifest(t, dataDir, game.ID), "the original was handed back")
			})
		}
	}
}

// conflictsName reports whether conflicts lists rel, won by the second mod.
func conflictsName(conflicts []core.ProfileConflict, rel string) bool {
	for _, c := range conflicts {
		if c.Path == rel && strings.HasSuffix(c.LoadOrderWinner.Key, "second") {
			return true
		}
	}
	return false
}

// Uninstalling the mod that lost the shared path leaves the winner's file
// where it is, and the original stays set aside under it; uninstalling the
// winner then puts the original back (#632). Before, the loser's removal
// took the winner's file and handed the original back while the winner
// still recorded the path.
func TestSharedPathOverAnOriginal_UninstallingTheLoserKeepsTheWinnersFile(t *testing.T) {
	for _, c := range sharedOriginalCases {
		for _, method := range rootMethods {
			t.Run(c.name+"/"+method.String(), func(t *testing.T) {
				ctx := context.Background()
				svc, dataDir := newOriginalsService(t)
				game := newRootGame(t, svc, method)
				dst := seedSharedOriginal(t, svc, game, c)
				_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)

				uplan, err := svc.PlanUninstall(ctx, game, "default", "src", "first", core.UninstallOptions{})
				require.NoError(t, err)
				for _, f := range uplan.Files {
					assert.False(t, strings.HasSuffix(f, c.rel), "the preview does not name the path the second mod holds: %q", f)
				}
				ures, err := svc.ApplyUninstall(ctx, game, uplan, core.UninstallOptions{})
				require.NoError(t, err)
				assertNoReplacedLinkClaim(t, ures.Warnings, "uninstall")
				assert.Equal(t, "the second mod's file", readFileString(t, dst), "the winner's file stays")
				require.Len(t, readOriginalsManifest(t, dataDir, game.ID), 1, "the original stays set aside")

				uplan, err = svc.PlanUninstall(ctx, game, "default", "src", "second", core.UninstallOptions{})
				require.NoError(t, err)
				_, err = svc.ApplyUninstall(ctx, game, uplan, core.UninstallOptions{})
				require.NoError(t, err)
				assert.Equal(t, "the game's own file", readFileString(t, dst), "the last mod's removal puts the original back")
				assert.Empty(t, readOriginalsManifest(t, dataDir, game.ID))
			})
		}
	}
}

// #630/#466/#469's protection is intact at a shared path: a file the user
// put where lmm deployed is still the user's - a redeploy leaves it, says
// so, and keeps the original lmm set aside.
func TestSharedPathOverAnOriginal_TheUsersReplacementIsStillKept(t *testing.T) {
	for _, c := range sharedOriginalCases {
		for _, method := range rootMethods {
			t.Run(c.name+"/"+method.String(), func(t *testing.T) {
				ctx := context.Background()
				svc, dataDir := newOriginalsService(t)
				game := newRootGame(t, svc, method)
				dst := seedSharedOriginal(t, svc, game, c)
				_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)

				// A new file, not an edit in place: a hard link's edit would
				// change the mod's cached copy too.
				require.NoError(t, os.Remove(dst))
				require.NoError(t, os.WriteFile(dst, []byte("the user's own file"), 0o644))

				res, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
				require.NoError(t, err)
				assert.Equal(t, "the user's own file", readFileString(t, dst), "the user's file is never silently overwritten")
				want := "its content changed after lmm deployed it"
				if method == domain.LinkSymlink {
					want = "you replaced lmm's link with your own file"
				}
				assert.True(t, anyContains(res.Warnings, want), "the deploy says why it left the file: %q", res.Warnings)
				require.Len(t, readOriginalsManifest(t, dataDir, game.ID), 1, "the original lmm set aside is still kept")
				assert.Equal(t, "the game's own file", readStoredOriginal(t, dataDir, game.ID, c.root, c.rel))
			})
		}
	}
}

func anyContains(msgs []string, want string) bool {
	for _, m := range msgs {
		if strings.Contains(m, want) {
			return true
		}
	}
	return false
}
