package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- OrderByProfile (Task 1) ---

// TestOrderByProfile table-drives core.OrderByProfile's contract: mods
// absent from profile.Mods come first (sorted by "SourceID:ID" key), then
// mods present in profile.Mods in that profile's own order - regardless of
// the input mods slice's order, and with duplicate keys collapsed to one
// occurrence.
func TestOrderByProfile(t *testing.T) {
	im := func(sourceID, id string) domain.InstalledMod {
		return domain.InstalledMod{Mod: domain.Mod{SourceID: sourceID, ID: id}}
	}
	ref := func(sourceID, id string) domain.ModReference {
		return domain.ModReference{SourceID: sourceID, ModID: id}
	}
	keysOf := func(mods []domain.InstalledMod) []string {
		keys := make([]string, len(mods))
		for i, m := range mods {
			keys[i] = domain.ModKey(m.SourceID, m.ID)
		}
		return keys
	}

	tests := []struct {
		name     string
		profile  *domain.Profile
		mods     []domain.InstalledMod
		expected []string
	}{
		{
			name:     "listed mods follow profile order regardless of input order",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "c"), ref("src", "a"), ref("src", "b")}},
			mods:     []domain.InstalledMod{im("src", "a"), im("src", "b"), im("src", "c")},
			expected: []string{"src:c", "src:a", "src:b"},
		},
		{
			name:     "unlisted mods sort first by key, then listed mods follow profile order",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "b")}},
			mods:     []domain.InstalledMod{im("src", "z"), im("src", "b"), im("src", "a")},
			expected: []string{"src:a", "src:z", "src:b"},
		},
		{
			name:     "empty profile sorts every mod by key",
			profile:  &domain.Profile{},
			mods:     []domain.InstalledMod{im("src", "c"), im("src", "a"), im("src", "b")},
			expected: []string{"src:a", "src:b", "src:c"},
		},
		{
			name:     "nil profile is treated as empty - sorts every mod by key",
			profile:  nil,
			mods:     []domain.InstalledMod{im("src", "c"), im("src", "a")},
			expected: []string{"src:a", "src:c"},
		},
		{
			name:     "a mod repeated in profile.Mods is not duplicated in the output",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "a"), ref("src", "a"), ref("src", "b")}},
			mods:     []domain.InstalledMod{im("src", "a"), im("src", "b")},
			expected: []string{"src:a", "src:b"},
		},
		{
			name:     "empty mods with a non-empty profile returns empty",
			profile:  &domain.Profile{Mods: []domain.ModReference{ref("src", "a")}},
			mods:     nil,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := core.OrderByProfileForTest(tt.profile, tt.mods)
			assert.Equal(t, tt.expected, keysOf(got))
		})
	}
}

// --- DeployProfile ---

// TestService_DeployProfile_MultiModDeploysInProfileOrder guards doDeploy's
// "no args" gathering step (GetInstalledModsInProfileOrder): deploy order
// must follow the profile's mod order, not DB insertion order or any other
// incidental ordering.
func TestService_DeployProfile_MultiModDeploysInProfileOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "c", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})

	// Profile order deliberately differs from DB insertion order (c, a, b).
	seedProfileWithMod(t, svc, "g1", "default", "src", "c", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.Deployed)
	assert.Empty(t, result.Skipped)

	var order []string
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.DeployDeployed {
			order = append(order, m.ModName)
		}
	}
	assert.Equal(t, []string{"Mod C", "Mod A", "Mod B"}, order, "deploy order must follow profile order")

	for _, f := range []string{"c.esp", "a.esp", "b.esp"} {
		_, err := os.Lstat(filepath.Join(gameDir, f))
		assert.NoError(t, err, "%s should be deployed", f)
	}
}

// TestService_DeployProfile_DeployOrderWinsFileConflicts guards Task 1's
// core motivation: two mods that each own a file at the same relative game
// path race for "who deploys last, wins" - and before this task, that race
// was decided by Go's randomized map iteration, not profile.Mods' documented
// load order (first = lowest priority). Deploying twice - once per profile
// order - proves the winner tracks profile order, not insertion order or
// chance: modY (deployed second, both times) wins the first pass, and after
// ReorderMods flips it to deploy first, modX wins instead.
func TestService_DeployProfile_DeployOrderWinsFileConflicts(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "modX", "Mod X", "1.0", true, map[string][]byte{"shared.esp": []byte("X-content")})
	seedNamedInstalledMod(t, svc, game, "src", "modY", "Mod Y", "1.0", true, map[string][]byte{"shared.esp": []byte("Y-content")})

	seedProfileWithMod(t, svc, "g1", "default", "src", "modX", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "modY", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Deployed)

	content, err := os.ReadFile(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, "Y-content", string(content), "modY deploys later (last in profile order) and must win the shared file")

	pm := svc.NewProfileManager()
	require.NoError(t, pm.ReorderMods(context.Background(), game.ID, "default", []domain.ModReference{
		{SourceID: "src", ModID: "modY", Version: "1.0"},
		{SourceID: "src", ModID: "modX", Version: "1.0"},
	}))

	result, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Deployed)

	content, err = os.ReadFile(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, "X-content", string(content), "after reordering the profile, modX now deploys later and must win the shared file")
}

// TestService_DeployProfile_LinkMethodOverrideHonored guards the --method
// override: DeployOptions.LinkMethod (a *domain.LinkMethod, not a bare
// value - see the task report for why) must both change how files are
// linked and be persisted via SetModLinkMethod.
func TestService_DeployProfile_LinkMethodOverrideHonored(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	override := domain.LinkCopy
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{LinkMethod: &override}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	info, err := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "override to copy method must not leave a symlink")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkCopy, mod.LinkMethod, "SetModLinkMethod must record the override")
}

// setProfileLinkMethod stamps an explicit link_method onto an existing profile
// file, as if the user had set it in the profile YAML by hand.
func setProfileLinkMethod(t *testing.T, svc *core.Service, gameID, profileName string, method domain.LinkMethod) {
	t.Helper()
	p, err := config.LoadProfile(svc.ConfigDir(), gameID, profileName)
	require.NoError(t, err)
	p.LinkMethod = method
	p.LinkMethodExplicit = true
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
}

// TestService_GetEffectiveLinkMethod_Precedence pins #81's documented
// resolution order: profile-explicit > game-explicit > global default
// (symlink in a fresh test service). A missing profile file must degrade to
// the game-level resolution, never error.
func TestService_GetEffectiveLinkMethod_Precedence(t *testing.T) {
	tests := map[string]struct {
		gameMethod     domain.LinkMethod
		gameExplicit   bool
		profileExists  bool
		profileMethod  *domain.LinkMethod
		expectedMethod domain.LinkMethod
	}{
		"profile beats game": {
			gameMethod: domain.LinkHardlink, gameExplicit: true,
			profileExists: true, profileMethod: linkMethodPtr(domain.LinkCopy),
			expectedMethod: domain.LinkCopy,
		},
		"explicit profile symlink beats non-symlink game": {
			gameMethod: domain.LinkCopy, gameExplicit: true,
			profileExists: true, profileMethod: linkMethodPtr(domain.LinkSymlink),
			expectedMethod: domain.LinkSymlink,
		},
		"game wins when profile is silent": {
			gameMethod: domain.LinkHardlink, gameExplicit: true,
			profileExists:  true,
			expectedMethod: domain.LinkHardlink,
		},
		"game wins when profile file is missing": {
			gameMethod: domain.LinkHardlink, gameExplicit: true,
			expectedMethod: domain.LinkHardlink,
		},
		"global default when neither is explicit": {
			gameMethod:     domain.LinkSymlink,
			profileExists:  true,
			expectedMethod: domain.LinkSymlink,
		},
	}

	for label, tc := range tests {
		t.Run(label, func(t *testing.T) {
			svc := newFlowsTestService(t)
			game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: tc.gameMethod, LinkMethodExplicit: tc.gameExplicit}

			if tc.profileExists {
				_, err := svc.NewProfileManager().Create(context.Background(), "g1", "default")
				require.NoError(t, err)
				if tc.profileMethod != nil {
					setProfileLinkMethod(t, svc, "g1", "default", *tc.profileMethod)
				}
			}

			method, err := svc.GetEffectiveLinkMethod(context.Background(), game, "default")
			require.NoError(t, err)
			assert.Equal(t, tc.expectedMethod, method)
		})
	}
}

// TestService_GetEffectiveLinkMethod_InvalidLinkMethodSurfaces pins #189: a
// hand-edited profile with an unrecognized link_method must surface as an
// error naming domain.ErrInvalidLinkMethod, not silently degrade to the
// game/global default the way a missing profile file does (the precedence
// test above). Without the fix, GetEffectiveLinkMethod would return the
// game's LinkHardlink with no error - the exact silent-misbehavior #172's
// fail-loud contract exists to prevent, one layer deeper on the deploy path.
func TestService_GetEffectiveLinkMethod_InvalidLinkMethodSurfaces(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkHardlink, LinkMethodExplicit: true}

	_, err := svc.NewProfileManager().Create(context.Background(), "g1", "default")
	require.NoError(t, err)
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilePath, append(data, []byte("link_method: bogus\n")...), 0644))

	_, err = svc.GetEffectiveLinkMethod(context.Background(), game, "default")

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
}

func linkMethodPtr(m domain.LinkMethod) *domain.LinkMethod {
	return &m
}

// TestService_DeployProfile_ProfileLinkMethodOverridesGame is #81's headline
// failing case: the game explicitly says symlink, the profile explicitly says
// copy, and the profile must win - the deployed file is a real copy, and
// SetModLinkMethod records the effective (profile) method.
func TestService_DeployProfile_ProfileLinkMethodOverridesGame(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	setProfileLinkMethod(t, svc, "g1", "default", domain.LinkCopy)

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	info, err := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "profile-level copy must beat the game's explicit symlink")

	mod, err := svc.GetInstalledMod(context.Background(), "src", "1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkCopy, mod.LinkMethod, "SetModLinkMethod must record the profile's effective method")
}

// TestService_DeployProfile_CLIMethodOverrideBeatsProfileLinkMethod pins the
// top of the precedence chain: the --method override (DeployOptions.LinkMethod)
// still beats a profile-level link_method.
func TestService_DeployProfile_CLIMethodOverrideBeatsProfileLinkMethod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	setProfileLinkMethod(t, svc, "g1", "default", domain.LinkCopy)

	override := domain.LinkSymlink
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{LinkMethod: &override}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deployed)

	info, err := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	require.NoError(t, err)
	assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "--method symlink must beat the profile's copy")
}

// TestService_DeployProfile_InvalidProfileLinkMethodFailsLoud is #189's
// deploy-path-level proof, one layer up from
// TestService_GetEffectiveLinkMethod_InvalidLinkMethodSurfaces: a profile
// with a hand-edited, unrecognized link_method must VISIBLY fail the deploy
// rather than silently deploying with the game's method. Before the fix,
// this would have deployed 1 mod with a real symlink at gameDir/plugin.esp;
// after, DeployProfile errors and nothing is written.
func TestService_DeployProfile_InvalidProfileLinkMethodFailsLoud(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink, LinkMethodExplicit: true}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")
	profilePath := filepath.Join(svc.ConfigDir(), "games", "g1", "profiles", "default.yaml")
	data, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profilePath, append(data, []byte("link_method: bogus\n")...), 0644))

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidLinkMethod)
	if result != nil {
		assert.Zero(t, result.Deployed, "an invalid link_method must deploy nothing, not fall back to the game's method")
	}
	_, statErr := os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.True(t, os.IsNotExist(statErr), "no file may be deployed when the effective link method can't be resolved")
}

// TestService_DeployProfile_PurgeRemovesFilesFirstAndPreservesEnabledSet
// guards --purge's two documented behaviors. The disabled mod is the key
// witness for "removed first": it is excluded from the redeploy pass
// entirely (never reaches the main per-mod loop, which also happens to
// undeploy-then-install), so its file's removal can only be explained by
// the purge pass itself. The enabled mod is the witness for "enabled set
// preserved": only it redeploys after the purge.
func TestService_DeployProfile_PurgeRemovesFilesFirstAndPreservesEnabledSet(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "kept-mod", "1.0", true, map[string][]byte{"kept.esp": []byte("k")})
	seedInstalledMod(t, svc, game, "src", "purged-mod", "1.0", false, map[string][]byte{"purged.esp": []byte("p")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "kept-mod", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "purged-mod", "1.0")

	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "purged-mod", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	purgedPath := filepath.Join(gameDir, "purged.esp")
	_, err := os.Lstat(purgedPath)
	require.NoError(t, err, "precondition: the disabled mod's file must be deployed before purge")

	var purgeTotal int
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{Purge: true}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.DeployPurging {
			purgeTotal = fe.EventScope().Total
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, purgeTotal, "purge must consider every installed mod, enabled or not")

	assert.Equal(t, 1, result.Deployed, "only the mod enabled before the purge should redeploy")
	_, err = os.Lstat(filepath.Join(gameDir, "kept.esp"))
	assert.NoError(t, err, "the previously-enabled mod should be redeployed after purge")
	_, err = os.Lstat(purgedPath)
	assert.True(t, os.IsNotExist(err), "the disabled mod's file must be removed by purge and never redeployed - proof purge actually undeploys mods excluded from the redeploy pass")
}

// TestService_DeployProfile_MissingCacheModRedownloads guards doDeploy's
// cache-miss path: when a mod's cache entry is gone, DeployProfile re-fetches
// it from source (GetMod -> GetModFiles -> DownloadMod) and still deploys it
// - a missing cache is not fatal to the mod, matching the pre-extraction CLI.
func TestService_DeployProfile_MissingCacheModRedownloads(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)

	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"plugin.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent) // mockSource.GetModFiles always returns file ID "1"

	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Redownload Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)

	// InstalledMod record exists, but nothing was ever stored in the cache.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          *mockMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	phases, _ := phasesOf(*seen)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	assert.Empty(t, result.Skipped)
	assert.Contains(t, phases, core.DeployRedownloading)

	_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
	assert.NoError(t, err, "redownloaded file should be deployed")
}

// TestService_DeployProfile_MissingCacheAndFetchFailure_SkipsMod guards the
// other half of the cache-miss path: when the redownload itself can't even
// start (GetMod fails - here because no source is registered for "src"),
// doDeploy skips that mod and continues rather than aborting.
func TestService_DeployProfile_MissingCacheAndFetchFailure_SkipsMod(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, nil)
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err, "a per-mod fetch failure must not fail the whole deploy")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Deployed)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Test Mod", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "failed to fetch")
}

// TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent
// guards finding D1: when the redownload itself starts (GetMod/GetModFiles
// succeed) but the actual file download fails, redeployFromSource must emit
// a DeployDownloadFailed event - not DeploySkipped - so cmd/lmm's dedicated
// DeployDownloadFailed handler (blank line / "✗ <mod> - <detail>" / blank
// line, matching the pre-extraction CLI) actually fires instead of
// DeploySkipped's bare, unpadded "✗" line. result.Skipped accounting must be
// unchanged (the mod still gets exactly one "<mod>: <reason>" entry), and
// DeploySkipped must NOT also fire for this mod - that would double-print
// under cmd/lmm's handler. Uses mockSourceWithDownloads without ever calling
// AddDownload, so its httptest server 404s the file request deterministically.
func TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	// Deliberately no AddDownload call - the mock's httptest server 404s any
	// file request, making the download fail deterministically.

	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Download Fail Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          *mockMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err, "a per-mod download failure must not fail the whole deploy")
	require.NotNil(t, result)

	assert.Equal(t, 0, result.Deployed)
	require.Len(t, result.Skipped, 1, "accounting must be unchanged: exactly one Skipped entry")
	assert.Equal(t, "Download Fail Mod", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "download failed:")

	var failEvt *core.ModEvent
	for _, e := range *seen {
		m, ok := e.(core.ModEvent)
		if !ok {
			continue
		}
		assert.NotEqual(t, core.DeploySkipped, m.Phase,
			"DeploySkipped must not also fire for a download failure - see DeploySkipped's doc comment ('a reason other than a hook or download failure') and cmd/lmm/deploy.go's DeploySkipped handler, which would double-print alongside DeployDownloadFailed's")
		if m.Phase == core.DeployDownloadFailed {
			failEvt = &m
		}
	}
	require.NotNil(t, failEvt, "DeployDownloadFailed event must fire on download failure")
	assert.Equal(t, "Download Fail Mod", failEvt.ModName)
	require.NotNil(t, failEvt.Mod)
	assert.Equal(t, "1", failEvt.Mod.ModID)
	assert.Contains(t, failEvt.Detail, "download failed:")
}

// TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError guards
// #95: when a mod's stored file IDs (mod.FileIDs) no longer match any file
// the source currently offers, redeployFromSource must fail that mod
// (result.Skipped + DeploySkipped, via selectDeployFiles's allowFallback=
// false) with a clear, actionable error - never silently substitute the
// primary file (the old DeployFallbackUsed phase, removed entirely in the
// renderer-cleanup task B2). Mirrors
// TestService_DeployProfile_MissingCacheAndDownloadFailure_EmitsDeployDownloadFailedEvent's
// negative-assertion idiom.
func TestService_DeployProfile_StoredFileIDsGone_SkipsModWithClearError(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	// Deliberately no AddDownload call needed - selectDeployFiles must fail
	// before any download is attempted.

	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Stale File Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)
	// mockSource.GetModFiles always returns a single file with ID "1" -
	// "stale-id" never matches it.

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          *mockMod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"stale-id"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err, "a per-mod stored-file-gone failure must not fail the whole deploy")
	require.NotNil(t, result)

	assert.Equal(t, 0, result.Deployed, "the mod must not be deployed via fallback substitution")
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0].Reason, "no longer available upstream")
	assert.Contains(t, result.Skipped[0].Reason, "stale-id")

	var sawSkipped bool
	phases, _ := phasesOf(*seen)
	for _, ph := range phases {
		if ph == core.DeploySkipped {
			sawSkipped = true
		}
	}
	assert.True(t, sawSkipped, "expected a DeploySkipped event")
}

// TestService_DeployProfile_StoredIDsGone_HealsToRecordedVersion is #96's
// DeployProfile-flavored healing guard (issue #96 names DeployProfile
// explicitly): the installed mod's stored FileIDs ("999") no longer match
// anything upstream, but its recorded Version ("1.0") still resolves to the
// source's archived file "9". With the cache dir missing (forcing
// redeployFromSource), the deploy must succeed using the recorded version's
// file rather than the #95 skip - never silently substituting the source's
// current primary file (1.5/"10").
func TestService_DeployProfile_StoredIDsGone_HealsToRecordedVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// DB row is pinned at 1.0 with stale FileIDs; nothing stored in the
	// cache, forcing redeployFromSource.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"999"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	phases, _ := phasesOf(*seen)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed, "must heal to the recorded version, not skip")
	assert.Empty(t, result.Skipped)
	assert.Contains(t, phases, core.DeployRedownloading)

	_, err = os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
	assert.NoError(t, err, "the recorded 1.0 file's payload should be deployed, not the source's current primary")
	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.True(t, os.IsNotExist(err), "the source's current primary (1.5) file must not be deployed via fallback")
}

// TestService_DeployProfile_StoredIDsGone_HealPersistsFileIDs is #139 item 1:
// a successful redeployFromSource heal must persist the healed FileIDs onto
// the DB row (via the targeted SetModFileIDs setter - never a full-row save),
// so `profile export` emits the live IDs instead of the dead ones and later
// cache misses resolve from the stored IDs' fast path instead of re-healing.
func TestService_DeployProfile_StoredIDsGone_HealPersistsFileIDs(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// DB row pinned at 1.0 with dead FileIDs; nothing in the cache, forcing
	// redeployFromSource to heal by version match (file "9").
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"999"},
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.Deployed)

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"9"}, installed.FileIDs,
		"a successful heal must persist the healed FileIDs, not keep the dead ones")

	data, err := svc.NewProfileManager().Export(context.Background(), "g1", "default")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "999",
		"profile export must no longer emit the dead FileIDs after a heal")
	assert.Contains(t, string(data), "9",
		"profile export must emit the healed FileID")
}

// TestService_DeployProfile_CacheMissRedownload_SameIDsPreserveChecksums is
// #139 item 1's checksum guard (the PR #128 SaveInstalledMod lesson applied
// to SetModFileIDs): when a cache-miss redownload resolves to the SAME stored
// FileIDs (no heal happened), the persist step must not rewrite the
// installed_mod_files rows - a blind delete+reinsert would silently drop
// their recorded checksums.
func TestService_DeployProfile_CacheMissRedownload_SameIDsPreserveChecksums(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := newTwoVersionSource(t)
	svc.RegisterSource(mock)

	// DB row whose stored FileIDs still match the source's 1.0 file ("9"),
	// with a recorded checksum; nothing in the cache, forcing a redownload
	// that resolves to the very same IDs.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"9"},
	}))
	require.NoError(t, svc.SaveFileChecksum(context.Background(), "src", "mod1", "g1", "default", "9", "abc123"))
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod1", "1.0")

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Deployed)

	files, err := svc.GetFilesWithChecksums(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "9", files[0].FileID)
	assert.Equal(t, "abc123", files[0].Checksum,
		"an unchanged FileIDs set must keep its recorded checksum through a cache-miss redownload")
}

// TestService_DeployProfile_HookOrder proves install.before_all ->
// install.before_each -> (deploy) -> install.after_each -> install.after_all
// ordering, mirroring TestService_UninstallMod_HookOrder.
func TestService_DeployProfile_HookOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	deployedFile := filepath.Join(gameDir, "plugin.esp")
	callLog := filepath.Join(scriptsDir, "calls.log")

	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each" >> `+callLog+`
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
if [ -e `+deployedFile+` ]; then
  echo "after_each:deployed" >> `+callLog+`
else
  echo "after_each:missing" >> `+callLog+`
fi
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all" >> `+callLog+`
exit 0`)

	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{
		BeforeAll: beforeAllScript, BeforeEach: beforeEachScript,
		AfterEach: afterEachScript, AfterAll: afterAllScript,
	}})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "before_all\nbefore_each\nafter_each:deployed\nafter_all\n", string(logContent))
}

// TestService_DeployProfile_BeforeEachHookFailure_SkipsModAndContinues
// guards deploy's before_each semantics, which differ from uninstall's: a
// failing install.before_each hook skips only that mod (added to
// result.Skipped) and the loop continues with the rest, rather than
// aborting the whole operation.
func TestService_DeployProfile_BeforeEachHookFailure_SkipsModAndContinues(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "bad", "Bad Mod", "1.0", true, map[string][]byte{"bad.esp": []byte("b")})
	seedNamedInstalledMod(t, svc, game, "src", "good", "Good Mod", "1.0", true, map[string][]byte{"good.esp": []byte("g")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "bad", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "good", "1.0")

	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "bad" ]; then
  echo "boom" >&2
  exit 1
fi
exit 0`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: beforeEachScript}})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err, "a before_each hook failure must skip that mod, not fail the deploy")
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Bad Mod", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "install.before_each hook failed")

	_, err = os.Lstat(filepath.Join(gameDir, "good.esp"))
	assert.NoError(t, err, "the other mod must still deploy")
}

// TestService_DeployProfile_BeforeAllHookFails_AbortsUnlessForce mirrors
// TestService_UninstallMod_BeforeAllHookFails_AbortsUnlessForce: a failing
// install.before_all hook aborts the whole deploy unless Force is set, in
// which case it becomes a Warning and the deploy proceeds.
func TestService_DeployProfile_BeforeAllHookFails_AbortsUnlessForce(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}})

	t.Run("fatal without Force", func(t *testing.T) {
		result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_all hook failed")
		require.NotNil(t, result)
		assert.Equal(t, 0, result.Deployed)

		_, err = os.Lstat(filepath.Join(gameDir, "plugin.esp"))
		assert.True(t, os.IsNotExist(err), "nothing should deploy on a fatal before_all failure")
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
			Force: true,
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.Deployed)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_all hook failed")
		assert.Contains(t, result.Warnings[0], "forced")
	})
}

// TestService_DeployProfile_AppliesProfileOverrides guards the final step of
// doDeploy: profile.Overrides (INI tweaks etc.) are written into the game's
// install directory via core.ApplyProfileOverrides after the deploy loop.
func TestService_DeployProfile_AppliesProfileOverrides(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	installDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, InstallPath: installDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	profile.Overrides = map[string][]byte{"tweaks.ini": []byte("[General]\nfoo=bar\n")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	content, err := os.ReadFile(filepath.Join(installDir, "tweaks.ini"))
	require.NoError(t, err)
	assert.Equal(t, "[General]\nfoo=bar\n", string(content))
}

// TestService_DeployProfile_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult
// guards the error-path convention from Task 2 (commit 45470e8): once the
// result struct exists, a later fatal error must still return it, not
// discard it. Here, a forced uninstall.before_all failure during --purge
// records a Warning, and the subsequent single-mod lookup (an unknown
// ModID) fails fatally - the Warning recorded during purge must still come
// back with the error.
func TestService_DeployProfile_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})

	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, ModID: "does-not-exist", SourceID: "src",
		Force: true,
	}, nil)
	require.Error(t, err, "an unknown ModID must fail the deploy")
	assert.Contains(t, err.Error(), "mod not found")
	require.NotNil(t, result, "diagnostics accumulated during purge must not be discarded")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "uninstall.before_all hook failed")
	assert.Contains(t, result.Warnings[0], "forced")
}

// TestService_DeployProfile_ProgressCallback_IndexTotalModNameSequence
// guards the Index/Total/ModName sequence a 3-mod deploy reports.
func TestService_DeployProfile_ProgressCallback_IndexTotalModNameSequence(t *testing.T) {
	svc, game := newDeployableService(t)
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})
	seedNamedInstalledMod(t, svc, game, "src", "3", "Mod Three", "1.0", true, map[string][]byte{"three.esp": []byte("3")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "2", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "3", "1.0")

	var seen []core.ModEvent
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, func(e core.Event) {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.DeployDeployed {
			seen = append(seen, m)
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, seen, 3)
	for i, p := range seen {
		assert.Equal(t, i+1, p.Index)
		assert.Equal(t, 3, p.Total)
	}
	assert.Equal(t, "Mod One", seen[0].ModName)
	assert.Equal(t, "Mod Two", seen[1].ModName)
	assert.Equal(t, "Mod Three", seen[2].ModName)
}

// TestService_DeployProfile_NilProgressCallbackIsSafe guards that progress
// may be nil per the required API.
func TestService_DeployProfile_NilProgressCallbackIsSafe(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	assert.NotPanics(t, func() {
		result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, 1, result.Deployed)
	})
}

// TestService_DeployProfile_SingleModByID guards the `lmm deploy <mod-id>`
// path: DeployOptions.ModID/SourceID restrict the deploy to a single mod,
// bypassing profile-order gathering entirely (no profile needs to exist).
func TestService_DeployProfile_SingleModByID(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Mod One", "1.0", true, map[string][]byte{"one.esp": []byte("1")})
	seedNamedInstalledMod(t, svc, game, "src", "2", "Mod Two", "1.0", true, map[string][]byte{"two.esp": []byte("2")})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{ModID: "1", SourceID: "src"}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)

	_, err = os.Lstat(filepath.Join(gameDir, "one.esp"))
	assert.NoError(t, err)
	_, err = os.Lstat(filepath.Join(gameDir, "two.esp"))
	assert.True(t, os.IsNotExist(err), "only the requested mod should deploy")
}

// TestService_DeployProfile_SingleModDisabled_RequiresAll guards doDeploy's
// disabled-single-mod guard: deploying a specific disabled ModID fails
// unless All is set.
func TestService_DeployProfile_SingleModDisabled_RequiresAll(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{ModID: "1", SourceID: "src"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Deployed)

	result, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{ModID: "1", SourceID: "src", All: true}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
}

// TestService_DeployProfile_ZeroModsToDeploy_NoHooksFired guards doDeploy's
// early return when nothing qualifies to deploy (here: a disabled mod with
// All unset): DeployProfile must return an empty result without firing any
// hooks at all, matching the pre-extraction CLI which returns before ever
// setting up hooks.
func TestService_DeployProfile_ZeroModsToDeploy_NoHooksFired(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", false, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: beforeAllScript}})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, result.Deployed)
	assert.Empty(t, result.Skipped)

	_, err = os.Stat(callLog)
	assert.True(t, os.IsNotExist(err), "install.before_all must not fire when there is nothing to deploy")
}

// --- Fix wave 1: progress-event positioning (review findings) ---
//
// The tests below guard the flow events added to restore the
// pre-extraction CLI's console positioning for diagnostics that Task 3
// correctly accumulated into DeployResult.Warnings/Notes but only surfaced
// via progress events for a subset of cases (DeployBeforeEachSkipped/
// DeployDownloadFailed/DeploySkipped). See the task-3-report.md "Fix wave 1"
// entry for the full mapping.

// TestService_DeployProfile_ForcedBeforeAllWarning_EmitsEventBeforeAnythingElse
// guards finding 1 (deploy side): a forced install.before_all failure must
// be reported via a DeployBeforeAllForced event before any other event -
// the pre-extraction CLI printed this warning as the very first line of
// output, before the "Deploying N mod(s)..." header.
func TestService_DeployProfile_ForcedBeforeAllWarning_EmitsEventBeforeAnythingElse(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Force: true,
	}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NotEmpty(t, *seen)
	phases, events := phasesOf(*seen)
	first, ok := events[0].(core.HookEvent)
	require.True(t, ok, "the forced before_all warning must be the first event emitted")
	assert.Equal(t, core.DeployBeforeAllForced, first.Phase, "the forced before_all warning must be the first event emitted")
	assert.Equal(t, "install.before_all", first.Stage)
	assert.Contains(t, first.Detail, "install.before_all hook failed")
	assert.Contains(t, first.Detail, "forced")
	assert.Equal(t, first.Detail, result.Warnings[0], "the event's Detail must match the recorded Warning text verbatim")

	require.Greater(t, len(phases), 1, "at least one later event (the mod itself deploying) must exist")
	assert.NotEqual(t, core.DeployBeforeAllForced, phases[1])
}

// TestService_DeployProfile_SkipHooks_RunsNoHooks guards Task 16: SkipHooks
// must suppress hook execution even though DeployProfile still resolves
// game-level hooks/a runner internally (the CLI's --no-hooks case) - no
// HookEvent is emitted and no hook-failure Warning is recorded, even though
// the configured before_all script would fail.
func TestService_DeployProfile_SkipHooks_RunsNoHooks(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		SkipHooks: true,
	}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)

	for _, e := range *seen {
		_, ok := e.(core.HookEvent)
		assert.False(t, ok, "SkipHooks must suppress every HookEvent")
	}
	assert.Empty(t, result.Warnings)
	assert.Equal(t, 1, result.Deployed, "the single seeded mod must actually deploy, not merely avoid hook errors")
}

// TestService_DeployProfile_PurgeForcedBeforeAllWarning_EmitsEventBeforePurgingEvent
// guards finding 1 (purge side): a forced uninstall.before_all failure
// during --purge must be reported before the DeployPurging event (which the
// CLI uses to print the "Purging N mod(s) before deploy..." header).
func TestService_DeployProfile_PurgeForcedBeforeAllWarning_EmitsEventBeforePurgingEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	failScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeAll: failScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, Force: true,
	}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)

	phases, events := phasesOf(*seen)
	require.GreaterOrEqual(t, len(events), 2)
	first, ok := events[0].(core.HookEvent)
	require.True(t, ok)
	assert.Equal(t, core.DeployBeforeAllForced, first.Phase)
	assert.Equal(t, "uninstall.before_all", first.Stage)
	assert.Contains(t, first.Detail, "uninstall.before_all hook failed")
	assert.Contains(t, first.Detail, "forced")
	assert.Equal(t, core.DeployPurging, phases[1], "the purge header event must come right after the forced warning")
}

// TestService_DeployProfile_PerModNoteDiagnostics_CarryModAttributionAndPrecedeSuccessEvent
// guards finding 3 (deploy loop): a failed SetModLinkMethod and a failed
// SetModDeployed both produce text with NO mod identity in it
// ("Warning: could not update link method: ..."), so position (via the
// event's ModName/ModID and its place in the event stream, before that same
// mod's DeployDeployed event) is the ONLY way to attribute either
// diagnostic to a mod. Two mods are seeded so a batched/misattributed
// implementation is distinguishable from a correctly-interleaved one: both
// mods' Note events must appear before THEIR OWN DeployDeployed event, not
// after both mods have already "succeeded".
//
// SetModLinkMethod/SetModDeployed are both plain UPDATEs against
// installed_mods, but Install/Uninstall also write to the DB (deployed_files,
// via SaveDeployedFile/DeleteDeployedFiles) - so a blanket write-lock
// (the BEGIN IMMEDIATE technique
// TestService_UninstallMod_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult
// uses) would fail Install itself before ever reaching SetModLinkMethod/
// SetModDeployed, defeating the test. Instead, a second connection installs
// a real SQLite trigger that aborts ONLY updates to installed_mods'
// link_method/deployed columns, leaving every other table (deployed_files)
// and every other installed_mods column untouched - deterministic, and
// narrow enough that Install/Uninstall still succeed normally.
func TestService_DeployProfile_PerModNoteDiagnostics_CarryModAttributionAndPrecedeSuccessEvent(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")

	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err, "SetModLinkMethod/SetModDeployed failures must not fail the deploy")
	require.NotNil(t, result)
	assert.Equal(t, 2, result.Deployed, "both mods must still deploy despite the bookkeeping failures")
	require.Len(t, result.Notes, 4, "2 mods x (link-method + mark-deployed) failures")

	// Find each mod's DeployDeployed index and confirm its two DeployNote
	// events (with matching ModName/ModID) both appear before it.
	for _, modName := range []string{"Mod A", "Mod B"} {
		var noteIdxs []int
		var deployedIdx = -1
		for i, e := range *seen {
			fe, ok := e.(core.FlowEvent)
			if !ok || fe.EventScope().ModName != modName {
				continue
			}
			switch fe.FlowPhase() {
			case core.DeployNote:
				noteIdxs = append(noteIdxs, i)
			case core.DeployDeployed:
				deployedIdx = i
			}
		}
		require.Len(t, noteIdxs, 2, "%s must have exactly 2 DeployNote events (link-method + mark-deployed)", modName)
		require.NotEqual(t, -1, deployedIdx, "%s must have a DeployDeployed event", modName)
		for _, ni := range noteIdxs {
			assert.Less(t, ni, deployedIdx, "%s's Note events must precede its own DeployDeployed event", modName)
		}
	}
}

// TestService_DeployProfile_UndeployFailureEmitsNoteEventBeforeSuccessEvent
// guards finding 3's third deploy-loop diagnostic (undeploy-before-redeploy
// failure, deploy.go's "Warning: undeploy %s: %v" - the only one of the
// three whose text DOES carry a mod name already), corrupting a previously
// deployed symlink into a plain file so the redeploy's own undeploy step
// fails deterministically, mirroring
// TestService_DisableMod_UndeployFailureIsNonFatal.
func TestService_DeployProfile_UndeployFailureEmitsNoteEventBeforeSuccessEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	require.Len(t, result.Notes, 1)
	assert.True(t, strings.HasPrefix(result.Notes[0], "Warning: undeploy Test Mod: "))

	require.Len(t, *seen, 2)
	phases, events := phasesOf(*seen)
	note, ok := events[0].(core.StepEvent)
	require.True(t, ok)
	assert.Equal(t, core.DeployNote, note.Phase)
	assert.Equal(t, "Test Mod", note.ModName)
	require.NotNil(t, note.Mod)
	assert.Equal(t, "1", note.Mod.ModID)
	assert.Equal(t, result.Notes[0], note.Detail)
	assert.Equal(t, core.DeployDeployed, phases[1], "the Note event must precede the success event")
}

// TestService_DeployProfile_PurgeBeforeEachSkip_EmitsWarningEventWithModAttribution
// guards finding 3's purge-side case: purgeForDeploy's before_each-skip
// diagnostic must fire a PurgeWarning event with the skipped mod's
// attribution, at the point it happens (inline with that mod), not batched.
func TestService_DeployProfile_PurgeBeforeEachSkip_EmitsWarningEventWithModAttribution(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "bad", "Bad Mod", "1.0", true, map[string][]byte{"bad.esp": []byte("b")})
	seedNamedInstalledMod(t, svc, game, "src", "good", "Good Mod", "1.0", true, map[string][]byte{"good.esp": []byte("g")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "bad", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "good", "1.0")

	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "bad" ]; then
  echo "boom" >&2
  exit 1
fi
exit 0`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: beforeEachScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, All: true,
	}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)

	var found *core.WarningEvent
	for _, e := range *seen {
		w, ok := e.(core.WarningEvent)
		if ok && w.Phase == core.PurgeWarning && w.ModName == "Bad Mod" {
			found = &w
			break
		}
	}
	require.NotNil(t, found, "expected a PurgeWarning event attributed to Bad Mod")
	require.NotNil(t, found.Mod)
	assert.Equal(t, "bad", found.Mod.ModID)
	assert.Contains(t, found.Message, "uninstall.before_each hook failed")
	assert.Contains(t, result.Warnings, found.Message, "the event's Message must match the recorded Warning text verbatim")
}

// TestService_DeployProfile_PurgeUndeployFailureEmitsNoteEvent guards
// finding 3's "finish the pattern" scope: purgeForDeploy's own per-mod ⚠
// undeploy-failure Note (previously batched, same as the deploy loop's
// equivalent) must fire inline via a PurgeNote event. Reuses the
// symlink-corruption technique, then triggers a --purge deploy so purge's
// own Uninstall call hits the same "not a symlink" failure.
func TestService_DeployProfile_PurgeUndeployFailureEmitsNoteEvent(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	deployedPath := filepath.Join(gameDir, "plugin.esp")
	require.NoError(t, os.Remove(deployedPath))
	require.NoError(t, os.WriteFile(deployedPath, []byte("not a symlink"), 0644))

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{Purge: true}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)

	var found *core.StepEvent
	for _, e := range *seen {
		step, ok := e.(core.StepEvent)
		if ok && step.Phase == core.PurgeNote {
			found = &step
			break
		}
	}
	require.NotNil(t, found, "expected a PurgeNote event for the purge-phase undeploy failure")
	assert.Equal(t, "Test Mod", found.ModName)
	assert.True(t, strings.HasPrefix(found.Detail, "⚠ Test Mod - "))
	assert.Contains(t, result.Notes, found.Detail)

	// PurgeNote must be emitted before DeployPurging's redeploy-phase
	// events (it belongs to the purge phase).
	purgingIdx, noteIdx := -1, -1
	phases, _ := phasesOf(*seen)
	for i, ph := range phases {
		if ph == core.DeployPurging {
			purgingIdx = i
		}
		if ph == core.PurgeNote && noteIdx == -1 {
			noteIdx = i
		}
	}
	require.NotEqual(t, -1, purgingIdx)
	assert.Greater(t, noteIdx, purgingIdx, "the purge-phase note must come after the DeployPurging header event, still within the purge phase")
}

// TestService_DeployProfile_PurgeBeforeEachSkip_WarningTextExact pins the
// deploy --purge before_each-skip Warning's exact wording (in particular
// its "during purge (not purged)" tail and NAME attribution), which
// deliberately differs from `lmm purge`'s treatment of the same skip
// (PurgeResult.Skipped + PurgeModSkipped) - a #61 divergence preserved
// through the shared-loop convergence.
func TestService_DeployProfile_PurgeBeforeEachSkip_WarningTextExact(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "bad", "Bad Mod", "1.0", true, map[string][]byte{"bad.esp": []byte("b")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "bad", "1.0")

	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{BeforeEach: beforeEachScript}})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true, All: true,
	}, nil)
	require.NoError(t, err)

	require.NotEmpty(t, result.Warnings)
	assert.True(t, strings.HasPrefix(result.Warnings[0], "uninstall.before_each hook failed for Bad Mod during purge (not purged): "),
		"got: %q", result.Warnings[0])
}

// TestService_DeployProfile_PurgeAfterEachWarning_UsesModID pins the
// deploy --purge after_each Warning's historical mod-ID attribution
// ("for <id>", not "for <name>") - previously untested wording, and the
// other side of a #61 divergence: `lmm purge` (doPurge, and now
// PurgeProfile) uses the mod NAME in the same warning.
func TestService_DeployProfile_PurgeAfterEachWarning_UsesModID(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "mod-id-1", "Test Mod", "1.0", true, map[string][]byte{"plugin.esp": []byte("d")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "mod-id-1", "1.0")

	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", "#!/bin/bash\necho boom >&2\nexit 1")
	seedHooks(t, svc, game, "default", domain.GameHooks{Uninstall: domain.HookConfig{AfterEach: afterEachScript}})

	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{
		Purge: true,
	}, nil)
	require.NoError(t, err)

	var found string
	for _, w := range result.Warnings {
		if strings.HasPrefix(w, "uninstall.after_each hook failed for ") {
			found = w
			break
		}
	}
	require.NotEmpty(t, found, "expected a purge-pass after_each warning; got %v", result.Warnings)
	assert.True(t, strings.HasPrefix(found, "uninstall.after_each hook failed for mod-id-1: "),
		"deploy --purge attributes by mod ID, got: %q", found)
}

// TestService_DeployProfile_OverridesWarningEmittedBeforeDeferredHookWarnings
// guards finding 2: the pre-extraction CLI printed the profile-overrides
// warning (computed and printed immediately once the deploy loop and
// install.after_all hook had already run) BEFORE its batched hook-warning
// print (install.after_each entries in mod order, then install.after_all) -
// even though, in both the pre-extraction CLI and this flow, after_each/
// after_all are computed earlier in the function than the overrides check.
// DeployProfile reproduces this by deferring the after_each/after_all
// DeployWarning events (queued, not emitted immediately) until after the
// overrides DeployWarning has been emitted - execution order (and the
// Warnings slice's append order) is unchanged; only the moment each event
// is *emitted* (and hence printed) is deferred.
func TestService_DeployProfile_OverridesWarningEmittedBeforeDeferredHookWarnings(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	installDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, InstallPath: installDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "1", "1.0", true, map[string][]byte{"plugin.esp": []byte("data")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	// An absolute override path is rejected by ApplyProfileOverrides
	// deterministically, with no filesystem trickery required.
	profile.Overrides = map[string][]byte{"/etc/passwd": []byte("x")}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "boom" >&2
exit 1`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{AfterEach: afterEachScript, AfterAll: afterAllScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed)
	require.Len(t, result.Warnings, 3, "after_each + after_all + overrides")

	overridesIdx, afterEachIdx, afterAllIdx := -1, -1, -1
	for i, e := range *seen {
		w, ok := e.(core.WarningEvent)
		if !ok || w.Phase != core.DeployWarning {
			continue
		}
		switch {
		case strings.Contains(w.Message, "applying profile overrides"):
			overridesIdx = i
		case strings.Contains(w.Message, "after_each"):
			afterEachIdx = i
		case strings.Contains(w.Message, "after_all"):
			afterAllIdx = i
		}
	}
	require.NotEqual(t, -1, overridesIdx, "expected an overrides DeployWarning event")
	require.NotEqual(t, -1, afterEachIdx, "expected an after_each DeployWarning event")
	require.NotEqual(t, -1, afterAllIdx, "expected an after_all DeployWarning event")
	assert.Less(t, overridesIdx, afterEachIdx, "overrides warning must be emitted before the after_each hook warning")
	assert.Less(t, overridesIdx, afterAllIdx, "overrides warning must be emitted before the after_all hook warning")
}

// TestService_DeployProfile_ContextCancelledBetweenMods_ReturnsPartialResultWithCtxErr
// guards Task 6 item d: DeployProfile's per-mod loop must check ctx between
// iterations, aborting BETWEEN mods (never mid-file-operation) with the
// established partial-result convention (see
// TestService_DeployProfile_FatalErrorAfterAccumulatedDiagnostic_ReturnsPartialResult) -
// the seam 5b's cancel-then-drain quit path relies on. The progress
// callback cancels ctx the instant the first mod's DeployDeployed event
// fires; the second and third mods must never be touched at all.
func TestService_DeployProfile_ContextCancelledBetweenMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true, map[string][]byte{"a.esp": []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true, map[string][]byte{"b.esp": []byte("b")})
	seedNamedInstalledMod(t, svc, game, "src", "c", "Mod C", "1.0", true, map[string][]byte{"c.esp": []byte("c")})
	seedProfileWithMod(t, svc, "g1", "default", "src", "a", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "b", "1.0")
	seedProfileWithMod(t, svc, "g1", "default", "src", "c", "1.0")

	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, func(e core.Event) {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.DeployDeployed && m.ModName == "Mod A" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result, "diagnostics/progress accumulated before cancellation must not be discarded")
	assert.Equal(t, 1, result.Deployed, "only the mod already fully deployed before cancellation was observed counts")

	_, err = os.Lstat(filepath.Join(gameDir, "a.esp"))
	assert.NoError(t, err, "Mod A must have fully deployed before cancellation")
	_, err = os.Lstat(filepath.Join(gameDir, "b.esp"))
	assert.True(t, os.IsNotExist(err), "Mod B must never have been touched - cancellation lands BETWEEN mods")
	_, err = os.Lstat(filepath.Join(gameDir, "c.esp"))
	assert.True(t, os.IsNotExist(err), "Mod C must never have been touched")
}

// --- Fix round 1: cancellation safety (task-3 review I1) ---

// TestService_DeployProfile_CancelledDuringLastModRedownload_RecordsSkipAndErrors
// is review finding I1's regression guard: redeployFromSource's ctx checks
// returned a bare `true`, which DeployProfile turns into a plain `continue`.
// When the cancelled mod is the LAST one in the profile there is no next
// iteration to trip the loop-head check, so the deploy reported success with
// the mod silently missing. Cancelling on the last mod's DeployRedownloading
// event (the callback immediately before its per-file download loop, so the
// loop-head check is what fires) must both record the skip and surface a
// context.Canceled error from DeployProfile.
func TestService_DeployProfile_CancelledDuringLastModRedownload_RecordsSkipAndErrors(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModMultiFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: map[string][]domain.DownloadableFile{
			"first": {{ID: "first-1", Name: "First File", FileName: "first.zip", Version: "1.0", IsPrimary: true}},
			"last":  {{ID: "last-1", Name: "Last File", FileName: "last.zip", Version: "1.0", IsPrimary: true}},
		},
	}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	stageInterplayDownload(t, mock, "first-1", "first.esp")
	stageInterplayDownload(t, mock, "last-1", "last.esp")
	mock.AddMod("g1", &domain.Mod{ID: "first", SourceID: "src", Name: "First Mod", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "last", SourceID: "src", Name: "Last Mod", Version: "1.0", GameID: "g1"})

	// Both rows exist with nothing cached, so each mod takes
	// redeployFromSource; the profile's order makes "last" the final one.
	for _, m := range []struct{ id, name, fileID string }{{"first", "First Mod", "first-1"}, {"last", "Last Mod", "last-1"}} {
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: m.id, SourceID: "src", Name: m.name, Version: "1.0", GameID: "g1"},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      []string{m.fileID},
		}))
		seedProfileWithMod(t, svc, "g1", "default", "src", m.id, "1.0")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.DeployRedownloading && fe.EventScope().ModName == "Last Mod" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed, "the first mod deployed before the cancellation")
	require.Len(t, result.Skipped, 1, "the cancelled mod must be recorded as skipped, not silently dropped")
	assert.Equal(t, "Last Mod", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "cancelled")
}

// TestService_QueriesRunDuringMutation pins the contract end to end under
// -race (v2 Phase 1 Task 6, #279; rewritten in fix round 1 per review
// finding I2): a DeployProfile holds the mutation slot open on a gate, 16
// query goroutines (GetInstalledMods + ListGames) must all complete while
// the gate is still closed, and a second mutation (SetModDeployed) started
// during that same window must NOT return until the gate opens. A
// regression that lets a query - or a second mutation - through
// unserialized deadlocks this test into one of its timeouts instead of
// passing green with nothing checked.
func TestService_QueriesRunDuringMutation(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()

	started := make(chan struct{})
	gate := make(chan struct{})
	errs := make(chan error, 32)

	deployDone := make(chan struct{})
	go func() {
		defer close(deployDone)
		first := true
		_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, func(core.Event) {
			if first {
				first = false
				close(started)
				<-gate
			}
		})
		errs <- err
	}()
	<-started

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.GetInstalledMods(ctx, game.ID, "default"); err != nil {
				errs <- err
			}
			_ = svc.ListGames()
		}()
	}
	queriesDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(queriesDone)
	}()
	select {
	case <-queriesDone:
	case <-time.After(5 * time.Second):
		t.Fatal("queries blocked behind a mutation")
	}

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		if err := svc.SetModDeployed(ctx, "src", "1", game.ID, "default", false); err != nil {
			errs <- err
		}
	}()

	select {
	case <-secondDone:
		t.Fatal("second mutation returned while the first mutation still holds the slot")
	case <-time.After(100 * time.Millisecond):
	}

	close(gate)

	select {
	case <-deployDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DeployProfile never finished after the gate was released")
	}
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second mutation never finished after the gate was released")
	}

	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
