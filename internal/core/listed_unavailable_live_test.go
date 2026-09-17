package core_test

// The #445 second gate's G2-3. The mod_path refusal says which listed mods
// no apply can deploy (ListedUnavailable), and blamed the cache for all of
// them: "the cache holds it only at 1.0, 2.0" for a mod listed at 2.0,
// which the cache does hold. What kept the apply from deploying it was
// another profile's live 1.0 (liveOtherVersion) - an enable cannot replace
// a deployed version, and a local mod has no source to download from. The
// refusal now names that profile and version, and the edit that works.

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

// liveOtherVersionState is V6b: alt, active, lists local:k at 2.0; q has
// k at 2.0 cached and not deployed; p has k at 1.0 deployed, recording
// Data/k.esp.
func liveOtherVersionState(t *testing.T, altVersion string) (*legacyFixture, string) {
	t.Helper()
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkCopy))
	dir := filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	doc := func(name, version string, active bool) {
		text := "name: " + name + "\ngame_id: sky\nmods:\n    - source_id: local\n      mod_id: k\n      version: \"" + version + "\"\n"
		if active {
			text += "is_default: true\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(text), 0o644))
	}
	doc("alt", altVersion, true)
	doc("p", "1.0", false)
	doc("q", "2.0", false)
	for _, row := range []struct {
		profile, version string
		deployed         bool
	}{{"p", "1.0", true}, {"q", "2.0", false}} {
		require.NoError(t, f.svc.GetGameCache(f.game).Store("sky", "local", "k", row.version, "Data/k.esp", []byte("k "+row.version)))
		require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: "k", SourceID: "local", Name: "K", Version: row.version, GameID: "sky"},
			ProfileName: row.profile, UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: row.deployed, LinkMethod: domain.LinkCopy,
		}))
	}
	live := filepath.Join(f.game.ModPath, "Data", "k.esp")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("k 1.0"), 0o644))
	require.NoError(t, f.svc.ExecForTest(ctx,
		`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES ('sky', 'p', 'Data/k.esp', 'local', 'k')`))
	return f, filepath.Join(dir, "alt.yaml")
}

func TestModPathRefusal_AListedVersionAnotherProfilesLiveVersionBlocksIsNamed(t *testing.T) {
	ctx := context.Background()

	t.Run("the refusal names the live version", func(t *testing.T) {
		f, path := liveOtherVersionState(t, "2.0")

		_, err := f.svc.SetGameModPath(ctx, "sky", filepath.Join(f.game.InstallPath, "mods2"))

		var inUse *core.GameModPathInUseError
		require.ErrorAs(t, err, &inUse)
		assert.Equal(t, []core.ListedVersionUnavailable{{
			SourceID: "local", ModID: "k", Version: "2.0", Cached: []string{"1.0", "2.0"},
			LiveVersion: "1.0", LiveProfile: "p", ProfileFile: path,
		}}, inUse.ListedUnavailable)
		text := err.Error()
		assert.NotContains(t, text, "the cache holds it only at", "the cache is not what blocks")
		assert.Contains(t, text, "local:k at version 2.0 (the cache holds it, but profile p has it deployed at 1.0, which only a download of 2.0 could replace, and lmm has no source local to download it from)")
		assert.Contains(t, text, "until you edit "+path+" to list it at the version deployed, 1.0 (and ask for this move again")
		assert.NotContains(t, text, "lmm profile apply")
	})

	t.Run("listing the live version clears it", func(t *testing.T) {
		f, _ := liveOtherVersionState(t, "1.0")

		refusals := followModPathRefusal(t, f.svc, "sky", filepath.Join(f.game.InstallPath, "mods2"))

		require.Len(t, refusals, 1)
		assert.True(t, strings.Contains(refusals[0], "`lmm profile apply alt --game sky`"), refusals[0])
		requireActiveListedLive(t, f.svc, "sky")
	})
}

// TestListedUnavailableText_SaysWhatEachEditIs pins the remedy clause when
// the listed mods are blocked for different reasons.
func TestListedUnavailableText_SaysWhatEachEditIs(t *testing.T) {
	err := &core.GameModPathInUseError{
		GameID: "sky", ModPath: "/m", NewModPath: "/n", DeployedFiles: 2, ActiveProfile: "alt",
		Profiles:         []core.ProfileDeployedFiles{{Profile: "p", DeployedFiles: 2}},
		ListedUnrecorded: 2,
		ListedUnavailable: []core.ListedVersionUnavailable{
			{SourceID: "local", ModID: "a", Version: "2.0", Cached: []string{"unknown"}, ProfileFile: "/alt.yaml"},
			{SourceID: "local", ModID: "k", Version: "2.0", Cached: []string{"1.0", "2.0"}, LiveVersion: "1.0", LiveProfile: "p", ProfileFile: "/alt.yaml"},
		},
	}
	assert.Contains(t, err.Error(), "local:a at version 2.0 (the cache holds it only at unknown, and lmm has no source local to download it from) and local:k at version 2.0 (the cache holds it, but profile p has it deployed at 1.0,")
	assert.Contains(t, err.Error(), "until you edit /alt.yaml to list each at a version the cache holds, or at the version deployed where another profile has one (and ask for this move again")
}
