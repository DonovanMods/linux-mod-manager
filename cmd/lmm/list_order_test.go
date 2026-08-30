package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modOrder returns each named mod's line index in out, in the order the
// names first appear - used to assert relative ordering without depending
// on exact column widths.
func modOrder(t *testing.T, out string, names ...string) []int {
	t.Helper()
	lines := strings.Split(out, "\n")
	indices := make([]int, len(names))
	for i, name := range names {
		indices[i] = -1
		for lineIdx, l := range lines {
			if strings.Contains(l, name) {
				indices[i] = lineIdx
				break
			}
		}
		require.NotEqual(t, -1, indices[i], "expected to find %q in output:\n%s", name, out)
	}
	return indices
}

// TestList_DisplaysProfileLoadOrder_NotInstallOrder guards #201: `lmm list`
// used to print mods in DB install order (installed_at), not the profile's
// load order that actually decides merge precedence. Mod A is installed
// before Mod B (install order: A, B) but the profile's load order is then
// reversed to [B, A] - the listing must follow the load order, not
// installed_at.
func TestList_DisplaysProfileLoadOrder_NotInstallOrder(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	seedDeployableMod(t, svc, game, "b", "Mod B", "b.esp")

	require.NoError(t, svc.NewProfileManager().ReorderMods(context.Background(), game.ID, "default", []domain.ModReference{
		{SourceID: "src", ModID: "b", Version: "1.0"},
		{SourceID: "src", ModID: "a", Version: "1.0"},
	}))

	t.Run("non-verbose", func(t *testing.T) {
		out := listNonVerbose(t, svc, game)
		idx := modOrder(t, out, "Mod B", "Mod A")
		assert.Less(t, idx[0], idx[1], "profile load order is [Mod B, Mod A] (Mod A is last - final say); the listing must follow that array order, not install order (which was A then B)")
	})

	t.Run("verbose", func(t *testing.T) {
		out := listVerbose(t, svc, game, false)
		idx := modOrder(t, out, "Mod B", "Mod A")
		assert.Less(t, idx[0], idx[1], "profile load order is [Mod B, Mod A] (Mod A is last - final say); the listing must follow that array order, not install order (which was A then B)")
	})

	t.Run("json", func(t *testing.T) {
		raw := listVerbose(t, svc, game, true)
		var out core.ModList
		require.NoError(t, json.Unmarshal([]byte(raw), &out))
		require.Len(t, out.Mods, 2)
		assert.Equal(t, "b", out.Mods[0].ID)
		assert.Equal(t, "a", out.Mods[1].ID)
	})
}

// TestList_ModMissingFromLoadOrder_StillShown guards the never-omit
// requirement (#201): GetInstalledModsInProfileOrder (deploy's seam)
// deliberately OMITS a mod absent from the profile's load order - correct
// for deploy, since an untracked mod must never silently deploy, but wrong
// for a listing, where every installed mod must still be visible. list.go
// uses core.OrderByProfile instead, which never omits: a load-order-absent
// mod is placed first (lowest priority - it has no claim to "final say"),
// never dropped.
func TestList_ModMissingFromLoadOrder_StillShown(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Tracked Mod", "a.esp")

	// Install "b" without ever adding it to the profile's load order -
	// simulates the edge case a normal add/remove flow shouldn't produce,
	// but which must not make the mod vanish from `lmm list`.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "b", SourceID: "src", Name: "Untracked Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	out := listNonVerbose(t, svc, game)
	assert.Contains(t, out, "Untracked Mod", "a mod absent from the profile's load order must still be listed")
	assert.Contains(t, out, "2 mod(s)", "both the tracked and untracked mod must count toward the total")
}
