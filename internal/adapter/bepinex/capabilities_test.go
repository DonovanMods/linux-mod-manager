package bepinex

// The capabilities U3 (#413) moved behind the seam, each tested at the
// interface core actually calls: ClaimArchive (the loader precondition),
// Verify (the loader INSTALLATION tier) and Guidance (the bootstrap
// advice). The layout table has its own file, moved from internal/core
// unmodified but for the package clause.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaimArchive_ClaimsOnlyTheUnmistakableShapes is the bar ClaimArchive
// is held to, and it is deliberately NARROWER than NormalizeArchive's.
//
// Core asks this of an adapter the game in hand does NOT use, so a claim
// becomes "your game needs a loader it does not have". The two shapes that
// name the directory `BepInEx` are claims no other game's mod could
// plausibly make. `plugins/` at an archive root, a loose `.dll` and a
// directory with an assembly in it are ordinary names, and claiming one
// would tell a 7 Days to Die user to install a loader they do not need -
// which is precisely the failure #358's `loaderDeclared` gate existed to
// prevent, preserved here rather than deleted with it.
func TestClaimArchive_ClaimsOnlyTheUnmistakableShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		members []string
		want    string // the evidence, or "" for no claim
	}{
		"shape A, a BepInEx root": {
			members: []string{"BepInEx/plugins/Foo.dll", "manifest.json"},
			want:    "game-root-relative",
		},
		"shape C, one wrapper around a BepInEx root": {
			members: []string{"MyPack/BepInEx/plugins/Foo.dll", "MyPack/manifest.json"},
			want:    "wrapped in a single directory",
		},
		"a mixed root is still unmistakably BepInEx": {
			members: []string{"BepInEx/patchers/Pre.dll", "Jotunn/Jotunn.dll"},
			want:    "game-root-relative",
		},
		"a bare plugins root is an ordinary directory name": {
			members: []string{"plugins/Foo.dll"},
		},
		"a loose root dll is an ordinary archive": {
			members: []string{"Foo.dll", "Foo.xml"},
		},
		"a plugin folder is an ordinary archive": {
			members: []string{"Jotunn/Jotunn.dll", "Jotunn/Jotunn.xml"},
		},
		"another game's mod folder is not claimed": {
			members: []string{"Mods/MyMod/ModInfo.xml", "Mods/MyMod/MyMod.dll"},
		},
		"an empty archive claims nothing": {},
	} {
		t.Run(name, func(t *testing.T) {
			claim, err := New().ClaimArchive(tc.members)
			require.NoError(t, err)
			assert.Equal(t, tc.want, claim.Evidence)
			assert.Equal(t, tc.want != "", claim.Claimed())
			if claim.Claimed() {
				assert.Equal(t, domain.LoaderKindBepInEx, claim.Requires,
					"a claim names the loader the game has to declare")
			}
		})
	}
}

// TestClaimArchive_AFrameworkPackIsRefusedRatherThanClaimed: the user's
// problem is different in kind. A plugin archive means "configure your game
// for BepInEx"; the BepInEx pack itself means "this is not a mod at all",
// and no amount of configuring makes it installable as one.
func TestClaimArchive_AFrameworkPackIsRefusedRatherThanClaimed(t *testing.T) {
	claim, err := New().ClaimArchive([]string{
		"BepInExPack/BepInEx/core/BepInEx.Preloader.dll",
		"BepInExPack/winhttp.dll",
		"manifest.json",
	})
	require.ErrorIs(t, err, adapter.ErrNotAMod)
	assert.False(t, claim.Claimed())
	assert.Contains(t, err.Error(), "--loader bepinex", "the refusal names the remedy")
}

// bepinexGame is a loader-DECLARING game rooted at dir.
func bepinexGame(dir string, loader *domain.GameLoader) *domain.Game {
	return &domain.Game{ID: "valheim", InstallPath: dir, ModPath: dir, Loader: loader}
}

// install writes a BepInEx installation into root: the preloader, the
// bootstrap files for mode, and (when logged) the LogOutput.log the loader
// writes on every run.
func install(t *testing.T, root, version string, mode domain.LoaderBootstrap, logged bool) {
	t.Helper()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	write(domain.BepInExPreloaderPath, "preloader")
	if version != "" {
		write(".doorstop_version", version+"\n")
	}
	switch mode {
	case domain.LoaderBootstrapNative:
		write(nativeScript, "#!/bin/sh\n")
		write(nativeDoorstop, "elf")
	case domain.LoaderBootstrapProton:
		write(protonProxy, "pe")
		write(protonConfig, "[General]\nenabled=true\n")
	case domain.LoaderBootstrapUnknown:
	}
	if logged {
		write(domain.BepInExLogPath, "[Info] BepInEx 5.4.23.5\n")
	}
}

// statuses lists a Verify result's statuses, so a case can say what the
// adapter reported without depending on order beyond what it asserts.
func statuses(findings []adapter.Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Status)
	}
	return out
}

// TestVerify_ReportsTheInstallationAgainstTheDeclaration covers the three
// checks that moved here, plus the two states that must report NOTHING.
func TestVerify_ReportsTheInstallationAgainstTheDeclaration(t *testing.T) {
	t.Run("a healthy installation reports nothing", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapNative, true)
		got := verifyGame(t, bepinexGame(root, &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
		}))
		assert.Empty(t, got, "the tier only speaks up when something is wrong")
	})

	t.Run("a game that declares no loader is not asked about one", func(t *testing.T) {
		// It is still a BepInEx game as far as core is concerned - the
		// preloader is right there, which is what resolved this adapter
		// (#424) - but every check below compares the installation against
		// a DECLARATION this user has not made.
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapNative, true)
		assert.Empty(t, verifyGame(t, bepinexGame(root, nil)))
	})

	t.Run("a missing preloader is the only thing reported", func(t *testing.T) {
		got := verifyGame(t, bepinexGame(t.TempDir(), &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
		}))
		require.Equal(t, []string{"loader_missing"}, statuses(got),
			"every check below asks about an installation that is not there")
		assert.False(t, got[0].Fixable)
		assert.NotEmpty(t, got[0].FixableReason)
		assert.Equal(t, adapter.SeverityIssue, got[0].Severity)
	})

	t.Run("version drift carries both halves", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.21.0", domain.LoaderBootstrapNative, true)
		got := verifyGame(t, bepinexGame(root, &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
		}))
		require.Equal(t, []string{"loader_version_mismatch"}, statuses(got))
		assert.Equal(t, "5.4.23.5", got[0].Recorded)
		assert.Equal(t, "5.4.21.0", got[0].Effective)
		assert.False(t, got[0].Fixable)
	})

	t.Run("an unreadable installed version suppresses the drift check", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "", domain.LoaderBootstrapNative, false)
		got := verifyGame(t, bepinexGame(root, &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
		}))
		assert.Empty(t, got, "the disk does not say, so there is no drift to report")
	})

	t.Run("the wrong pack for the declared bootstrap", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapProton, true)
		got := verifyGame(t, bepinexGame(root, &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
		}))
		require.Equal(t, []string{"loader_bootstrap_incomplete"}, statuses(got))
		assert.Contains(t, got[0].Note, nativeScript)
		assert.False(t, got[0].Fixable)
	})

	t.Run("an undeclared bootstrap has no expectation to compare against", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapUnknown, true)
		got := verifyGame(t, bepinexGame(root, &domain.GameLoader{Kind: domain.LoaderKindBepInEx}))
		assert.Empty(t, got)
	})
}

// verifyGame runs the adapter's Verify for game.
func verifyGame(t *testing.T, game *domain.Game) []adapter.Finding {
	t.Helper()
	got, err := New().Verify(context.Background(), adapter.VerifyRequest{Game: game})
	require.NoError(t, err)
	return got
}

// TestVerify_IsReadOnly is the Verifier contract: an adapter reports, core
// repairs, and nothing here may touch the game directory.
func TestVerify_IsReadOnly(t *testing.T) {
	root := t.TempDir()
	install(t, root, "5.4.21.0", domain.LoaderBootstrapProton, false)
	before := treeOf(t, root)

	verifyGame(t, bepinexGame(root, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
	}))
	assert.Equal(t, before, treeOf(t, root))
}

// treeOf lists root's regular files, slash-separated and sorted.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		require.NoError(t, rerr)
		out = append(out, filepath.ToSlash(rel))
		return nil
	}))
	return out
}

// TestGuidance_NamesWhatIsStillMissing walks the three states a user meets
// on the way to a working BepInEx install, in order.
func TestGuidance_NamesWhatIsStillMissing(t *testing.T) {
	declared := &domain.GameLoader{Kind: domain.LoaderKindBepInEx}

	t.Run("nothing installed", func(t *testing.T) {
		notes := New().Guidance(bepinexGame(t.TempDir(), declared))
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0].Title, "not installed")
	})

	t.Run("installed but never run", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapNative, false)
		notes := New().Guidance(bepinexGame(root, declared))
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0].Title, "has not run")
		assert.Contains(t, notes[0].Body, "lmm game show valheim",
			"the exact launch string lives in LoaderStatus, so the note names the command that prints it")
	})

	t.Run("installed but undeclared", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapNative, true)
		notes := New().Guidance(bepinexGame(root, nil))
		require.Len(t, notes, 1)
		assert.Equal(t, undeclaredNotice(bepinexGame(root, nil)), notes[0].Body)
	})

	t.Run("a working install has nothing to say", func(t *testing.T) {
		root := t.TempDir()
		install(t, root, "5.4.23.5", domain.LoaderBootstrapNative, true)
		assert.Empty(t, New().Guidance(bepinexGame(root, declared)))
	})

	t.Run("a nil game is not a panic", func(t *testing.T) {
		assert.Empty(t, New().Guidance(nil))
	})
}

// TestNormalizeArchive_NoticesAnUndeclaredLoaderOnlyWhenItActed is #424's
// notice: lmm acted on BepInEx it FOUND rather than on the user's
// configuration, so it names the command that makes the answer permanent -
// but only when the rules actually did something, and never for a game that
// has already declared the loader.
func TestNormalizeArchive_NoticesAnUndeclaredLoaderOnlyWhenItActed(t *testing.T) {
	members := []string{"BepInEx/plugins/Foo.dll", "manifest.json"}
	unplaceable := []string{"Data/StreamingAssets/thing.bundle"}

	t.Run("undeclared and placed: the notice is on the layout", func(t *testing.T) {
		game := bepinexGame(t.TempDir(), nil)
		layout := normalizeFor(t, game, members)
		assert.Contains(t, layout.Warnings, undeclaredNotice(game))
	})

	t.Run("undeclared and NOT placed: nothing useful was said, so nothing is", func(t *testing.T) {
		game := bepinexGame(t.TempDir(), nil)
		layout := normalizeFor(t, game, unplaceable)
		assert.NotContains(t, layout.Warnings, undeclaredNotice(game))
	})

	t.Run("declared: there is nothing for this user to do", func(t *testing.T) {
		game := bepinexGame(t.TempDir(), &domain.GameLoader{Kind: domain.LoaderKindBepInEx})
		assert.Empty(t, normalizeFor(t, game, members).Warnings)
	})
}

// normalizeFor runs NormalizeArchive for game.
func normalizeFor(t *testing.T, game *domain.Game, members []string) adapter.Layout {
	t.Helper()
	layout, err := New().NormalizeArchive(adapter.NormalizeRequest{Game: game, ModName: "Foo", Members: members})
	require.NoError(t, err)
	return layout
}
