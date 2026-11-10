package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/d2r"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// d2rFixture is a Diablo II: Resurrected game (#546) - adapter d2r, started
// through Battle.net - whose default profile holds the given mods, each
// cached as <folder>/<folder>.mpq/modinfo.json, in that load order.
func d2rFixture(t *testing.T, mods ...[2]string) (*core.Service, *fakeLauncher, *domain.Game) {
	t.Helper()
	g := launchGame("diablo2resurrected")
	g.Name = "Diablo II: Resurrected"
	g.Adapter = d2r.ID
	g.Launch = &domain.GameLaunch{Via: domain.LaunchViaBattleNet, BattleNet: &domain.BattleNetLaunch{Prefix: bnetPrefix(t, false), Product: "OSI"}}
	svc, f := launchFixture(t, g)
	svc.RegisterAdapter(d2r.New())
	svc.RegisterLaunchMethod(core.BattleNetLaunchMethod())
	f.path["wine"] = "/usr/bin/wine"
	game := mustGame(t, svc, g.ID)

	ctx := context.Background()
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	for _, m := range mods {
		name, folder := m[0], m[1]
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", name, "1", folder+"/"+folder+".mpq/modinfo.json", []byte("{}")))
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:         domain.Mod{ID: name, SourceID: "src", Name: name, Version: "1", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
		}))
		p.Mods = append(p.Mods, domain.ModReference{SourceID: "src", ModID: name, Version: "1"})
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
	return svc, f, game
}

// #546: a deploy of a D2R profile ends by saying exactly what to enter in
// Battle.net's "Additional command line arguments" - the mod last in load
// order, since D2R loads one - and a launch says it again. Nothing starts:
// the fake launcher only records.
func TestD2R_DeployAndLaunchAdviseTheModArgument(t *testing.T) {
	svc, f, game := d2rFixture(t, [2]string{"Loot Filter", "Loot"}, [2]string{"Stash Tabs", "Stash"})
	ctx := context.Background()

	var warned []string
	res, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, warningsIn(&warned))
	require.NoError(t, err)
	assert.Equal(t, 2, res.Deployed)
	require.Len(t, res.Warnings, 2)
	assert.Contains(t, res.Warnings[0], "-mod Stash -txt")
	assert.Contains(t, res.Warnings[0], "Additional command line arguments")
	assert.Contains(t, res.Warnings[1], "one mod at a time")
	assert.True(t, containsLine(warned, "-mod Stash -txt"), "the advice is an event too, so the CLI prints it as it goes: %v", warned)

	launched, err := svc.Launch(ctx, game, core.LaunchOptions{})
	require.NoError(t, err)
	assert.Equal(t, res.Warnings, launched.Warnings)
	assert.Len(t, f.runs, 1)
}

// A profile with no D2R mod deployed has nothing to advise, and a game on
// any other adapter never does.
func TestD2R_NoAdviceWithoutAMod(t *testing.T) {
	svc, _, game := d2rFixture(t)
	res, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	launched, err := svc.Launch(context.Background(), game, core.LaunchOptions{})
	require.NoError(t, err)
	assert.Empty(t, launched.Warnings)
}

// #546: the deploy that drops the last D2R mod says to take the -mod the
// previous one advised back out of Battle.net's settings - once: the next
// deploy, with nothing advised since, says nothing.
func TestD2R_DeployWithoutTheModSaysToRemoveIt(t *testing.T) {
	svc, _, game := d2rFixture(t, [2]string{"Loot Filter", "Loot"})
	ctx := context.Background()

	res, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "-mod Loot -txt")

	_, err = svc.DisableMod(ctx, game, "default", "src", "Loot Filter")
	require.NoError(t, err)
	res, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "remove -mod Loot -txt")

	res, err = svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
}

// #546: a profile apply and a profile switch deploy too, so they end with
// the same advice a deploy does.
func TestD2R_ProfileApplyAndSwitchAdviseTheModArgument(t *testing.T) {
	svc, _, game := d2rFixture(t, [2]string{"Loot Filter", "Loot"})
	ctx := context.Background()

	// The mod is off, and the profile document - the desired state - is
	// edited back to on: the apply enables and deploys it.
	_, err := svc.DisableMod(ctx, game, "default", "src", "Loot Filter")
	require.NoError(t, err)
	p, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	p.Mods = []domain.ModReference{{SourceID: "src", ModID: "Loot Filter", Version: "1"}}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))

	plan, err := svc.PlanProfileApply(ctx, game, "default")
	require.NoError(t, err)
	applied, err := svc.ApplyProfileApply(ctx, game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, containsLine(applied.Warnings, "-mod Loot -txt"), "%v", applied.Warnings)

	// A second profile holding a different D2R mod: switching to it says
	// what to enter now, in place of the -mod the apply advised.
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "Stash Tabs", "1", "Stash/Stash.mpq/modinfo.json", []byte("{}")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "Stash Tabs", SourceID: "src", Name: "Stash Tabs", Version: "1", GameID: game.ID},
		ProfileName: "other", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "other", GameID: game.ID,
		Mods: []domain.ModReference{{SourceID: "src", ModID: "Stash Tabs", Version: "1"}},
	}))
	switchPlan, err := svc.PlanProfileSwitch(ctx, game, "other")
	require.NoError(t, err)
	switched, err := svc.ApplyProfileSwitch(ctx, game, switchPlan, nil)
	require.NoError(t, err)
	assert.True(t, containsLine(switched.Warnings, "-mod Stash -txt", "in place of -mod Loot -txt"), "%v", switched.Warnings)
}
