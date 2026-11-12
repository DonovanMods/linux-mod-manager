package core_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #605: a game's Wine/Proton prefix. Every fixture here is a temp-dir tree;
// the Steam libraries are handed to the Service through its seam, so
// nothing reads the machine's own Steam.

var (
	prefixOlder = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prefixNewer = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
)

// prefixTree builds a Wine prefix at dir with a users directory per name
// (Public included by every caller that wants it), and returns dir.
func prefixTree(t *testing.T, dir string, users ...string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "drive_c", "users"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "system.reg"), nil, 0o644))
	for _, u := range users {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "drive_c", "users", u), 0o755))
	}
	return dir
}

// compatdataTree builds <lib>/steamapps/compatdata/<appID>/pfx as Proton
// leaves it - steamuser and Public, a version file beside pfx - stamped
// mod, and returns the pfx path.
func compatdataTree(t *testing.T, lib, appID string, mod time.Time) string {
	t.Helper()
	pfx := prefixTree(t, filepath.Join(lib, "steamapps", "compatdata", appID, "pfx"), "steamuser", "Public")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(pfx), "version"), []byte("9.0-4\n"), 0o644))
	for _, p := range []string{filepath.Join(pfx, "system.reg"), pfx} {
		require.NoError(t, os.Chtimes(p, mod, mod))
	}
	return pfx
}

// prefixGame is a game whose Steam app id lmm knows from launch.steam_app_id.
func prefixGame(appID string) *domain.Game {
	g := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE", InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data"}
	if appID != "" {
		g.Launch = &domain.GameLaunch{SteamAppID: appID}
	}
	return g
}

// prefixService is a Service whose Steam libraries are libs.
func prefixService(t *testing.T, libs ...string) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.SetSteamLibraries(func() []string { return libs })
	return svc
}

func TestResolvePrefix(t *testing.T) {
	base := t.TempDir()
	libA, libB, empty := filepath.Join(base, "libA"), filepath.Join(base, "libB"), filepath.Join(base, "empty")
	require.NoError(t, os.MkdirAll(filepath.Join(empty, "steamapps"), 0o755))
	pfxA := compatdataTree(t, libA, "489830", prefixOlder)
	pfxB := compatdataTree(t, libB, "489830", prefixNewer)
	onlyB := compatdataTree(t, libB, "377160", prefixOlder)

	single := prefixTree(t, filepath.Join(base, "heroic"), "dyoung", "Public")
	several := prefixTree(t, filepath.Join(base, "lutris"), "alice", "bob", "Public")
	none := prefixTree(t, filepath.Join(base, "none"), "Public")

	// #605 review: user directories that are symlinks - one out of the
	// prefix (to a stand-in for the host's home), one that stays inside.
	home := filepath.Join(base, "host-home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	escapes := prefixTree(t, filepath.Join(base, "escapes"), "Public")
	require.NoError(t, os.Symlink(home, filepath.Join(escapes, "drive_c", "users", "dyoung")))
	linkedIn := prefixTree(t, filepath.Join(base, "linked-in"), "Public")
	require.NoError(t, os.MkdirAll(filepath.Join(linkedIn, "drive_c", "users", "Public", "dyoung"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("Public", "dyoung"), filepath.Join(linkedIn, "drive_c", "users", "dyoung")))
	// An explicit prefix that is itself a symlink to a real prefix.
	prefixLink := filepath.Join(base, "prefix-link")
	require.NoError(t, os.Symlink(single, prefixLink))
	// A compatdata/<id> folder named where its pfx belongs.
	compatFolder := filepath.Dir(onlyB)

	for _, tc := range []struct {
		name       string
		libs       []string
		game       func() *domain.Game
		path       string
		origin     string
		library    string
		candidates int
		user       string
		userOrigin string
		resolved   bool
		problem    string
	}{
		{
			name: "compatdata in one library of several",
			libs: []string{libA, empty, libB}, game: func() *domain.Game { return prefixGame("377160") },
			path: onlyB, origin: core.PrefixOriginSteam, library: libB,
			user: "steamuser", userOrigin: core.PrefixUserProton, resolved: true,
		},
		{
			name: "several libraries: the most recently used wins",
			libs: []string{libA, libB}, game: func() *domain.Game { return prefixGame("489830") },
			path: pfxB, origin: core.PrefixOriginSteam, library: libB, candidates: 2,
			user: "steamuser", userOrigin: core.PrefixUserProton, resolved: true,
		},
		{
			name: "compatdata missing everywhere",
			libs: []string{empty}, game: func() *domain.Game { return prefixGame("489830") },
			problem: "no Steam compatdata prefix for app 489830",
		},
		{
			name: "no Steam app id",
			libs: []string{libA, libB}, game: func() *domain.Game { return prefixGame("") },
			problem: "no Steam app id",
		},
		{
			name: "an explicit prefix wins over compatdata",
			libs: []string{libA, libB}, game: func() *domain.Game { g := prefixGame("489830"); g.Prefix = single; return g },
			path: single, origin: core.PrefixOriginExplicit,
			user: "dyoung", userOrigin: core.PrefixUserOnly, resolved: true,
		},
		{
			name: "an explicit prefix that does not exist is not replaced by compatdata",
			libs: []string{libA, libB}, game: func() *domain.Game {
				g := prefixGame("489830")
				g.Prefix = filepath.Join(base, "gone")
				return g
			},
			path: filepath.Join(base, "gone"), origin: core.PrefixOriginExplicit,
			problem: "does not exist",
		},
		{
			name: "an explicit prefix that is not a Wine prefix",
			libs: nil, game: func() *domain.Game { g := prefixGame(""); g.Prefix = base; return g },
			path: base, origin: core.PrefixOriginExplicit,
			problem: "not a Wine prefix",
		},
		{
			name: "a relative explicit prefix",
			libs: nil, game: func() *domain.Game { g := prefixGame(""); g.Prefix = "Prefixes/skyrim"; return g },
			path: "Prefixes/skyrim", origin: core.PrefixOriginExplicit,
			problem: "absolute",
		},
		{
			name: "several users, no prefix_user",
			libs: nil, game: func() *domain.Game { g := prefixGame(""); g.Prefix = several; return g },
			path: several, origin: core.PrefixOriginExplicit,
			problem: "alice, bob",
		},
		{
			name: "several users, prefix_user picks one",
			libs: nil, game: func() *domain.Game {
				g := prefixGame("")
				g.Prefix, g.PrefixUser = several, "bob"
				return g
			},
			path: several, origin: core.PrefixOriginExplicit,
			user: "bob", userOrigin: core.PrefixUserExplicit, resolved: true,
		},
		{
			name: "no user but Public",
			libs: nil, game: func() *domain.Game { g := prefixGame(""); g.Prefix = none; return g },
			path: none, origin: core.PrefixOriginExplicit,
			problem: "no user directory",
		},
		{
			name: "a prefix_user that is not there",
			libs: nil, game: func() *domain.Game {
				g := prefixGame("")
				g.Prefix, g.PrefixUser = single, "someone"
				return g
			},
			path: single, origin: core.PrefixOriginExplicit, user: "someone", userOrigin: core.PrefixUserExplicit,
			problem: "drive_c/users/someone",
		},
		{
			name: "a prefix_user that is not one directory name",
			libs: nil, game: func() *domain.Game {
				g := prefixGame("")
				g.Prefix, g.PrefixUser = single, "../dyoung"
				return g
			},
			path: single, origin: core.PrefixOriginExplicit, user: "../dyoung", userOrigin: core.PrefixUserExplicit,
			problem: "prefix_user",
		},
		{
			name: "prefix_user overrides Proton's steamuser",
			libs: []string{libB}, game: func() *domain.Game {
				g := prefixGame("377160")
				g.PrefixUser = "Public"
				return g
			},
			path: onlyB, origin: core.PrefixOriginSteam, library: libB,
			user: "Public", userOrigin: core.PrefixUserExplicit, resolved: true,
		},
		{
			name: "the steamworkshop mapping names the app too",
			libs: []string{libB}, game: func() *domain.Game {
				g := prefixGame("")
				g.SourceIDs = map[string]string{"steamworkshop": "377160"}
				return g
			},
			path: onlyB, origin: core.PrefixOriginSteam, library: libB,
			user: "steamuser", userOrigin: core.PrefixUserProton, resolved: true,
		},
		{
			name: "a found user directory that links out of the prefix",
			game: func() *domain.Game { g := prefixGame(""); g.Prefix = escapes; return g },
			path: escapes, origin: core.PrefixOriginExplicit,
			problem: "resolves outside the prefix's drive_c/users",
		},
		{
			name: "a prefix_user that links out of the prefix",
			game: func() *domain.Game {
				g := prefixGame("")
				g.Prefix, g.PrefixUser = escapes, "dyoung"
				return g
			},
			path: escapes, origin: core.PrefixOriginExplicit, user: "dyoung", userOrigin: core.PrefixUserExplicit,
			problem: "resolves outside the prefix's drive_c/users",
		},
		{
			name: "a user directory that links within drive_c/users",
			game: func() *domain.Game {
				g := prefixGame("")
				g.Prefix, g.PrefixUser = linkedIn, "dyoung"
				return g
			},
			path: linkedIn, origin: core.PrefixOriginExplicit,
			user: "dyoung", userOrigin: core.PrefixUserExplicit, resolved: true,
		},
		{
			name: "an explicit prefix that is a symlink to one",
			game: func() *domain.Game { g := prefixGame(""); g.Prefix = prefixLink; return g },
			path: prefixLink, origin: core.PrefixOriginExplicit,
			user: "dyoung", userOrigin: core.PrefixUserOnly, resolved: true,
		},
		{
			name: "an explicit prefix that is a compatdata folder, not its pfx",
			game: func() *domain.Game { g := prefixGame(""); g.Prefix = compatFolder; return g },
			path: compatFolder, origin: core.PrefixOriginExplicit,
			problem: "append /pfx",
		},
		{
			name: "an explicit prefix with surrounding whitespace",
			game: func() *domain.Game { g := prefixGame(""); g.Prefix = single + " "; return g },
			path: single + " ", origin: core.PrefixOriginExplicit,
			problem: "leading or trailing whitespace",
		},
		{
			name: "a prefix_user with surrounding whitespace",
			game: func() *domain.Game {
				g := prefixGame("")
				g.Prefix, g.PrefixUser = single, " dyoung"
				return g
			},
			path: single, origin: core.PrefixOriginExplicit, user: " dyoung", userOrigin: core.PrefixUserExplicit,
			problem: "leading or trailing whitespace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := prefixService(t, tc.libs...)
			got := svc.ResolvePrefix(tc.game())
			require.NotNil(t, got)
			assert.Equal(t, tc.path, got.Path)
			assert.Equal(t, tc.origin, got.Origin)
			assert.Equal(t, tc.library, got.Library)
			assert.Len(t, got.Candidates, tc.candidates)
			assert.Equal(t, tc.user, got.User)
			assert.Equal(t, tc.userOrigin, got.UserOrigin)
			assert.Equal(t, tc.resolved, got.Resolved)
			if tc.resolved {
				assert.Empty(t, got.Problem)
				assert.Equal(t, filepath.Join(got.Path, "drive_c", "users", got.User), got.UserPath)
			} else {
				assert.Contains(t, got.Problem, tc.problem)
			}
			assert.False(t, got.Needed, "no shipped adapter needs a prefix")
			assert.Empty(t, got.Finding, "a game that needs no prefix gets no finding")
		})
	}

	t.Run("the choice among several is reported", func(t *testing.T) {
		got := prefixService(t, libA, libB).ResolvePrefix(prefixGame("489830"))
		require.Len(t, got.Candidates, 2)
		assert.Equal(t, pfxB, got.Candidates[0].Path)
		assert.Equal(t, pfxA, got.Candidates[1].Path)
		assert.True(t, got.Candidates[0].ModifiedAt.Equal(prefixNewer))
		assert.Contains(t, got.Note, "2 Steam libraries")
		assert.Contains(t, got.Note, "most recently used")
	})
}

// usesPrefix is a generic adapter that declares #605's capability - the
// stand-in for #355's bethesda adapter until it exists.
type usesPrefix struct{ adapter.Generic }

func (usesPrefix) ID() string                   { return "prefix-test" }
func (usesPrefix) UsesPrefix(*domain.Game) bool { return true }

// TestResolvePrefix_FindingOnlyForAGameThatNeedsOne: unresolved is a
// finding, never an error, and only for a game whose adapter needs the
// prefix.
func TestResolvePrefix_FindingOnlyForAGameThatNeedsOne(t *testing.T) {
	svc := prefixService(t)
	svc.RegisterAdapter(usesPrefix{})

	game := prefixGame("489830")
	game.Adapter = "prefix-test"
	got := svc.ResolvePrefix(game)
	assert.True(t, got.Needed)
	assert.False(t, got.Resolved)
	assert.Equal(t, core.FindingPrefixUnresolved, got.Finding)

	resolved := prefixTree(t, filepath.Join(t.TempDir(), "pfx"), "dyoung")
	game.Prefix = resolved
	got = svc.ResolvePrefix(game)
	assert.True(t, got.Needed)
	assert.True(t, got.Resolved)
	assert.Empty(t, got.Finding)
}

// TestGameDetail_CarriesThePrefix: `lmm game show`'s document carries the
// resolution for a game with a prefix to show, and no member for a game
// with none that needs none - which keeps game_detail.golden as it was.
func TestGameDetail_CarriesThePrefix(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	pfx := compatdataTree(t, lib, "489830", prefixOlder)
	svc := prefixService(t, lib)
	ctx := context.Background()

	game := prefixGame("489830")
	game.SourceIDs = map[string]string{"nexusmods": "skyrimspecialedition"}
	require.NoError(t, svc.SaveGame(ctx, game))
	detail, err := svc.GameDetail(ctx, game.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.Prefix)
	assert.Equal(t, pfx, detail.Prefix.Path)
	assert.Equal(t, core.PrefixOriginSteam, detail.Prefix.Origin)

	other := &domain.Game{ID: "native", Name: "Native", InstallPath: "/games/native", ModPath: "/games/native", SourceIDs: map[string]string{"nexusmods": "native"}}
	require.NoError(t, svc.SaveGame(ctx, other))
	detail, err = svc.GameDetail(ctx, other.ID)
	require.NoError(t, err)
	assert.Nil(t, detail.Prefix, "nothing found, nothing set, nothing needed: no member")
}

// TestVerify_ReportsAnUnresolvedPrefixAsAWarning: verify's adapter tier
// carries the finding for a game that needs a prefix - a warning, so it
// does not fail the run - and says nothing for a game that needs none.
func TestVerify_ReportsAnUnresolvedPrefixAsAWarning(t *testing.T) {
	svc := prefixService(t)
	svc.RegisterAdapter(usesPrefix{})
	ctx := context.Background()

	dir := t.TempDir()
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE", InstallPath: dir, ModPath: dir, Adapter: "prefix-test",
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(ctx, game))
	report, err := svc.VerifyReport(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	result := report.Result
	var found *core.VerifyFinding
	for i := range result.Findings {
		if result.Findings[i].Status == core.VerifyStatusPrefixUnresolved {
			found = &result.Findings[i]
		}
	}
	require.NotNil(t, found, "%+v", result.Findings)
	assert.Contains(t, found.Note, "no Steam app id")
	assert.NotEmpty(t, found.FixableReason)
	assert.Equal(t, 1, result.Warnings)
	assert.Zero(t, result.Issues)

	game.Adapter = ""
	require.NoError(t, svc.SaveGame(ctx, game))
	report, err = svc.VerifyReport(ctx, game, "default", core.VerifyOptions{}, nil)
	require.NoError(t, err)
	for _, f := range report.Result.Findings {
		assert.NotEqual(t, core.VerifyStatusPrefixUnresolved, f.Status)
	}
}

// TestResolvePrefix_ConcurrentWithSetSteamLibraries: the composition root
// sets the library seam once, but nothing stops a test or a frontend from
// setting it while queries run; under -race this is the proof that the seam
// is safe to read and replace concurrently.
func TestResolvePrefix_ConcurrentWithSetSteamLibraries(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib")
	pfx := compatdataTree(t, lib, "489830", prefixOlder)
	svc := prefixService(t, lib)
	game := prefixGame("489830")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				got := svc.ResolvePrefix(game)
				if got.Path != "" {
					assert.Equal(t, pfx, got.Path)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := range 50 {
				if (i+j)%2 == 0 {
					svc.SetSteamLibraries(func() []string { return []string{lib} })
				} else {
					svc.SetSteamLibraries(nil)
				}
			}
		}()
	}
	wg.Wait()
}
